package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	"github.com/woyin/orangeguard/internal/config"
	"github.com/woyin/orangeguard/internal/engine"
	"github.com/woyin/orangeguard/internal/ui"
)

const pluginIdentifier = "orangeguard"

// pluginVersion is injected by release builds via -ldflags.
var pluginVersion = "dev"

// bypassHeader marks nested executions issued by this plugin. The host already
// skips the calling plugin's router for host.model.* callbacks; the header is
// a second line of defence against recursion on hosts that do not.
const bypassHeader = "X-Orangeguard-Bypass"

var eng = engine.New(cpaHost{})

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ModelRegistrar        bool     `json:"model_registrar"`
	ModelRouter           bool     `json:"model_router"`
	Executor              bool     `json:"executor"`
	ExecutorModelScope    string   `json:"executor_model_scope"`
	ExecutorInputFormats  []string `json:"executor_input_formats"`
	ExecutorOutputFormats []string `json:"executor_output_formats"`
	ManagementAPI         bool     `json:"management_api"`
	UsagePlugin           bool     `json:"usage_plugin"`
}

type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcModelRouteRequest struct {
	pluginapi.ModelRouteRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcManagementRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type managementRoute struct {
	Method      string
	Path        string
	Description string `json:",omitempty"`
}

type resourceRoute struct {
	Path        string
	Menu        string
	Description string `json:",omitempty"`
}

// uiResourcePath is the management page, served under
// /v0/resource/plugins/orangeguard/ui and listed in the cpa menu.
const uiResourcePath = "/ui"

// Formats the executor accepts and returns. Requests are replayed through the
// host in their original client format, so no translation happens here.
var executorFormats = []string{"openai", "openai-response", "claude", "gemini"}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown:
		return okEnvelope(map[string]any{})
	case pluginabi.MethodModelRegister:
		return okEnvelope(engine.ModelRegistration(eng.Config()))
	case pluginabi.MethodModelRoute:
		return routeModel(request)
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(map[string]string{"identifier": eng.Config().Provider})
	case pluginabi.MethodExecutorExecute:
		return execute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return executeStream(request)
	case pluginabi.MethodExecutorCountTokens:
		return countTokens(request)
	case pluginabi.MethodManagementRegister:
		return okEnvelope(map[string]any{
			"routes": []managementRoute{
				{Method: http.MethodGet, Path: engine.StatusPath, Description: "orangeguard configuration, monitor, guard/virtual-model and cooldown status"},
				{Method: http.MethodPost, Path: engine.ResetPath, Description: "Clear cooldowns: body {\"model\":\"name\"} or empty for all"},
				{Method: http.MethodPost, Path: engine.MonitorResetPath, Description: "Clear served-model monitor statistics"},
			},
			"resources": []resourceRoute{
				{Path: uiResourcePath, Menu: "OrangeGuard", Description: "Virtual models, downgrade guard, monitor and cooldowns"},
			},
		})
	case pluginabi.MethodUsageHandle:
		return handleUsage(request)
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method, 0), nil
	}
}

func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	cfg, warnings, errParse := config.Parse(req.ConfigYAML)
	if errParse != nil {
		return errParse
	}
	eng.SetConfig(cfg)
	for _, warning := range warnings {
		cpaHost{}.Log("", "warn", "orangeguard: config: "+warning)
	}
	return nil
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginIdentifier,
			Version:          pluginVersion,
			Author:           "woyin",
			GitHubRepository: "https://github.com/woyin/orangeguard",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "When false the plugin declines every request."},
				{Name: "provider", Type: pluginapi.ConfigFieldTypeString, Description: "Provider id virtual models are listed under (default orangeguard)."},
				{Name: "guard", Type: pluginapi.ConfigFieldTypeObject, Description: "Model substitution guard: max_retries, retry_delay_ms, on_missing_model, models[{model, expect, deny, max_retries}]."},
				{Name: "cooldown", Type: pluginapi.ConfigFieldTypeObject, Description: "Cooldown seconds per failure kind (quota, rate_limit, auth, not_found, server_error, mismatch), backoff and cap."},
				{Name: "virtual_models", Type: pluginapi.ConfigFieldTypeArray, Description: "Merged models: name, strategy (fallback|round-robin|random|weighted), members[{model, weight, expect}], capabilities."},
			},
		},
		Capabilities: registrationCapability{
			ModelRegistrar:        true,
			ModelRouter:           true,
			Executor:              true,
			ExecutorModelScope:    string(pluginapi.ExecutorModelScopeBoth),
			ExecutorInputFormats:  executorFormats,
			ExecutorOutputFormats: executorFormats,
			ManagementAPI:         true,
			UsagePlugin:           true,
		},
	}
}

func routeModel(raw []byte) ([]byte, error) {
	var req rpcModelRouteRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	bypass := req.Headers != nil && strings.TrimSpace(req.Headers.Get(bypassHeader)) != ""
	claimed, reason := eng.Claims(req.RequestedModel, bypass)
	if !claimed {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	return okEnvelope(pluginapi.ModelRouteResponse{
		Handled:    true,
		TargetKind: pluginapi.ModelRouteTargetSelf,
		Reason:     reason,
	})
}

func engineRequest(req rpcExecutorRequest) engine.Request {
	body := req.OriginalRequest
	if len(body) == 0 {
		body = req.Payload
	}
	return engine.Request{
		CallbackID:   req.HostCallbackID,
		SourceFormat: formatOrDefault(req.SourceFormat, req.Format),
		Format:       formatOrDefault(req.Format, req.SourceFormat),
		Model:        strings.TrimSpace(req.Model),
		Alt:          req.Alt,
		Body:         body,
		Headers:      req.Headers,
		Query:        req.Query,
	}
}

func execute(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	resp, failure := eng.Execute(engineRequest(req))
	if failure != nil {
		return errorEnvelope(failure.Code, failure.Message, failure.Status), nil
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: resp.Body, Headers: resp.Headers})
}

type streamSink struct{ streamID string }

func (s streamSink) Emit(payload []byte) error {
	_, errCall := callHost(pluginabi.MethodHostStreamEmit, map[string]any{
		"stream_id": s.streamID,
		"payload":   payload,
	})
	return errCall
}

func closePluginStream(streamID, errMsg string) {
	_, _ = callHost(pluginabi.MethodHostStreamClose, map[string]any{
		"stream_id": streamID,
		"error":     strings.TrimSpace(errMsg),
	})
}

func executeStream(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	streamID := strings.TrimSpace(req.StreamID)
	if streamID == "" {
		return errorEnvelope("executor_error", "stream_id is required for executor.execute_stream", 0), nil
	}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				closePluginStream(streamID, fmt.Sprintf("orangeguard panic: %v", recovered))
			}
		}()
		if errRun := eng.ExecuteStream(engineRequest(req), streamSink{streamID: streamID}); errRun != nil {
			// The host turns the close error into the terminating SSE error
			// event, so the plugin must not emit one itself.
			closePluginStream(streamID, errRun.Error())
			return
		}
		closePluginStream(streamID, "")
	}()
	return okEnvelope(map[string]any{
		"headers": http.Header{"Content-Type": []string{"text/event-stream"}},
	})
}

func countTokens(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	body := req.OriginalRequest
	if len(body) == 0 {
		body = req.Payload
	}
	payload, _ := json.Marshal(map[string]int{"input_tokens": engine.EstimateTokens(body)})
	return okEnvelope(pluginapi.ExecutorResponse{Payload: payload})
}

// handleUsage feeds cpa usage records to the passive served-model monitor.
func handleUsage(raw []byte) ([]byte, error) {
	var record pluginapi.UsageRecord
	if errUnmarshal := json.Unmarshal(raw, &record); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	eng.RecordUsage(record.Model, record.Alias, record.ResponseModel, record.Failed)
	return okEnvelope(map[string]any{})
}

func handleManagement(raw []byte) ([]byte, error) {
	var req rpcManagementRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if strings.HasPrefix(req.Path, "/v0/resource/") {
		// The page itself is public; every action it takes goes through
		// /v0/management with the operator's management key.
		if strings.TrimRight(req.Path, "/") != "/v0/resource/plugins/"+pluginIdentifier+uiResourcePath {
			return okEnvelope(pluginapi.ManagementResponse{StatusCode: http.StatusNotFound, Body: []byte("not found")})
		}
		return okEnvelope(pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Content-Type":            []string{"text/html; charset=utf-8"},
				"Cache-Control":           []string{"no-store"},
				"X-Content-Type-Options":  []string{"nosniff"},
				"Content-Security-Policy": []string{"default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'self'"},
			},
			Body: ui.Page,
		})
	}
	status, body := eng.HandleManagement(strings.ToUpper(req.Method), req.Path, req.Query, req.Body)
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       body,
	})
}

func formatOrDefault(primary, fallback string) string {
	if value := strings.TrimSpace(primary); value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}
