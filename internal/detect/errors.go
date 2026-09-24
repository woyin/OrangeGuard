package detect

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Kind classifies why an upstream attempt failed.
type Kind string

const (
	// KindClient is a request problem (400/413/422...) that another model
	// would most likely reject as well. No cooldown, no failover by default.
	KindClient Kind = "client_error"
	// KindQuota means the account/model has run out of credit or quota.
	KindQuota Kind = "quota"
	// KindRateLimit is a short-lived 429.
	KindRateLimit Kind = "rate_limit"
	// KindAuth is 401/403 without quota wording.
	KindAuth Kind = "auth"
	// KindNotFound means the upstream does not know the model.
	KindNotFound Kind = "not_found"
	// KindServer is a 5xx, timeout or transport failure.
	KindServer Kind = "server_error"
	// KindMismatch means a different model served the request.
	KindMismatch Kind = "model_mismatch"
)

var quotaMarkers = []string{
	"insufficient_quota", "insufficient quota", "quota", "usage limit", "usage_limit",
	"usage_limit_reached", "billing", "credit", "balance", "exceeded your current",
	"limit reached", "resource_exhausted", "resource exhausted", "payment required",
	"plan limit", "out of tokens", "token limit exceeded for", "daily limit", "monthly limit",
	"weekly limit", "额度", "余额", "配额", "用量",
}

var rateMarkers = []string{
	"rate limit", "rate_limit", "ratelimit", "too many requests", "429", "overloaded",
	"slow down", "请求过于频繁", "限流",
}

var authMarkers = []string{
	"unauthorized", "invalid api key", "invalid_api_key", "authentication", "permission",
	"forbidden", "401", "403", "token expired", "invalid token",
}

var notFoundMarkers = []string{
	"model_not_found", "model not found", "does not exist", "unknown model", "no such model",
	"unsupported model", "not supported model", "model is not available", "unknown provider for model",
	"no auth available", "no available auth", "auth_not_found", "模型不存在",
}

// Classify maps an upstream status code and error message to a Kind. status
// may be 0 when only a message is available (for example a mid-stream error),
// in which case the message alone decides.
func Classify(status int, message string) Kind {
	m := strings.ToLower(message)
	quota := containsAny(m, quotaMarkers)
	switch {
	case status == 402:
		return KindQuota
	case status == 429:
		if quota {
			return KindQuota
		}
		return KindRateLimit
	case status == 401:
		return KindAuth
	case status == 403:
		if quota {
			return KindQuota
		}
		return KindAuth
	case status == 404:
		return KindNotFound
	case status == 408 || status >= 500:
		if quota {
			return KindQuota
		}
		return KindServer
	case status >= 400:
		if quota {
			return KindQuota
		}
		if containsAny(m, notFoundMarkers) {
			return KindNotFound
		}
		return KindClient
	}
	// No status: infer from the message.
	switch {
	case quota:
		return KindQuota
	case containsAny(m, rateMarkers):
		return KindRateLimit
	case containsAny(m, notFoundMarkers):
		return KindNotFound
	case containsAny(m, authMarkers):
		return KindAuth
	default:
		return KindServer
	}
}

// Failover reports whether a failure of this kind should move a virtual model
// on to its next member.
func (k Kind) Failover(onClientError bool) bool {
	if k == KindClient {
		return onClientError
	}
	return true
}

var (
	reSeconds    = regexp.MustCompile(`"?(?:resets?_in_seconds|retry_after_seconds|retry_after|retryafter|reset_seconds)"?\s*[:=]\s*"?(\d+(?:\.\d+)?)`)
	reRetryDelay = regexp.MustCompile(`"?retrydelay"?\s*[:=]\s*"(\d+(?:\.\d+)?)s"`)
	reRetryIn    = regexp.MustCompile(`(?:retry|try again|reset[s]?|available again)\s+(?:after|in)\s+(?:about\s+)?(\d+(?:\.\d+)?)\s*(ms|milliseconds?|s|secs?|seconds?|m|mins?|minutes?|h|hrs?|hours?)\b`)
	reDuration   = regexp.MustCompile(`(?:retry|try again|reset[s]?)\s+(?:after|in)\s+((?:\d+h)?(?:\d+m)?(?:\d+(?:\.\d+)?s)?)\b`)
	reResetsAt   = regexp.MustCompile(`"?(?:resets_at|reset_at|resetsat)"?\s*[:=]\s*"?(\d{10})`)
)

// RetryAfter extracts a reset hint from an upstream error message. It
// understands JSON fields (resets_in_seconds, retry_after, Gemini retryDelay,
// resets_at unix timestamps) and prose ("try again in 20 minutes",
// "retry after 1h30m"). It returns 0 when nothing usable is found.
func RetryAfter(message string, now time.Time) time.Duration {
	m := strings.ToLower(message)
	if match := reSeconds.FindStringSubmatch(m); match != nil {
		if secs, errParse := strconv.ParseFloat(match[1], 64); errParse == nil && secs > 0 {
			return time.Duration(secs * float64(time.Second))
		}
	}
	if match := reRetryDelay.FindStringSubmatch(m); match != nil {
		if secs, errParse := strconv.ParseFloat(match[1], 64); errParse == nil && secs > 0 {
			return time.Duration(secs * float64(time.Second))
		}
	}
	if match := reResetsAt.FindStringSubmatch(m); match != nil {
		if unix, errParse := strconv.ParseInt(match[1], 10, 64); errParse == nil {
			if d := time.Unix(unix, 0).Sub(now); d > 0 {
				return d
			}
		}
	}
	if match := reRetryIn.FindStringSubmatch(m); match != nil {
		value, errParse := strconv.ParseFloat(match[1], 64)
		if errParse == nil && value > 0 {
			unit := match[2]
			switch {
			case strings.HasPrefix(unit, "ms") || strings.HasPrefix(unit, "milli"):
				return time.Duration(value * float64(time.Millisecond))
			case strings.HasPrefix(unit, "h"):
				return time.Duration(value * float64(time.Hour))
			case strings.HasPrefix(unit, "m"):
				return time.Duration(value * float64(time.Minute))
			default:
				return time.Duration(value * float64(time.Second))
			}
		}
	}
	if match := reDuration.FindStringSubmatch(m); match != nil && match[1] != "" {
		if d, errParse := time.ParseDuration(match[1]); errParse == nil && d > 0 {
			return d
		}
	}
	return 0
}

// IsHostCooldown reports whether the error was generated by cpa itself
// because every credential for the model is in cpa's own cooldown. Its
// reset_seconds describes cpa's credential backoff (often only seconds), not
// when the upstream quota actually resets.
func IsHostCooldown(message string) bool {
	m := strings.ToLower(message)
	return strings.Contains(m, `"code":"model_cooldown"`) || strings.Contains(m, `"code": "model_cooldown"`)
}

func containsAny(s string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}
