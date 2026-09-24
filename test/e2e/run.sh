#!/usr/bin/env bash
# End-to-end test: builds CLIProxyAPI at the SDK version pinned in go.mod,
# loads the orangeguard plugin into it, points it at a mock upstream and checks
# guard, virtual-model failover, cooldown skipping, streaming and management.
#
# Usage: test/e2e/run.sh path/to/orangeguard.so
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
PLUGIN="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
WORK="$ROOT/test/e2e/.work"
CPA_PORT=18317
MOCK_PORT=18080
GOOS="$(go env GOOS)"
GOARCH="$(go env GOARCH)"

mkdir -p "$WORK/plugins/$GOOS/$GOARCH" "$WORK/auth"
cp "$PLUGIN" "$WORK/plugins/$GOOS/$GOARCH/"

CPA_VERSION="$(cd "$ROOT" && go list -m -f '{{.Version}}' github.com/router-for-me/CLIProxyAPI/v7)"
if [ ! -x "$WORK/cpa-$CPA_VERSION" ]; then
  echo "building CLIProxyAPI $CPA_VERSION ..."
  SRC="$(cd "$ROOT" && go mod download -json "github.com/router-for-me/CLIProxyAPI/v7@$CPA_VERSION" | python3 -c 'import json,sys; print(json.load(sys.stdin)["Dir"])')"
  rm -rf "$WORK/cpa-src" && cp -r "$SRC" "$WORK/cpa-src" && chmod -R u+w "$WORK/cpa-src"
  (cd "$WORK/cpa-src" && CGO_ENABLED=1 go build -o "$WORK/cpa-$CPA_VERSION" ./cmd/server)
fi

cat > "$WORK/config.yaml" <<EOF
port: $CPA_PORT
host: "127.0.0.1"
auth-dir: "$WORK/auth"
api-keys: ["test-key"]
request-retry: 0
remote-management:
  allow-remote: false
  secret-key: "mgmt-key"
  disable-control-panel: true
openai-compatibility:
  - name: "mock"
    base-url: "http://127.0.0.1:$MOCK_PORT/v1"
    api-key-entries:
      - api-key: "sk-mock"
    models:
      - name: "gpt-6-astra"
      - name: "flaky"
      - name: "quota-model"
      - name: "good-model"
plugins:
  enabled: true
  dir: "$WORK/plugins"
  configs:
    orangeguard:
      enabled: true
      guard:
        max_retries: 2
        retry_delay_ms: 10
        models:
          - model: "gpt-6-astra"
          - model: "flaky"
      virtual_models:
        - name: "smart"
          strategy: fallback
          members:
            - model: "quota-model"
            - model: "gpt-6-astra"
            - model: "good-model"
          capabilities:
            display_name: "Smart (merged)"
            context_length: 200000
            max_output_tokens: 32000
            vision: true
EOF

python3 "$ROOT/test/e2e/mock_upstream.py" "$MOCK_PORT" 2> "$WORK/upstream.log" &
MOCK_PID=$!
"$WORK/cpa-$CPA_VERSION" -config "$WORK/config.yaml" > "$WORK/cpa.log" 2>&1 &
CPA_PID=$!
trap 'kill $MOCK_PID $CPA_PID 2>/dev/null || true' EXIT

for _ in $(seq 1 60); do
  curl -sf -o /dev/null "http://127.0.0.1:$CPA_PORT/v1/models" -H "Authorization: Bearer test-key" && break
  sleep 0.5
done

FAILED=0
check() { # name, expected substring, actual
  if [[ "$3" == *"$2"* ]]; then echo "PASS  $1"; else echo "FAIL  $1: expected '$2' in: $3"; FAILED=1; fi
}
chat() { # model, stream
  curl -s -w '\nHTTP=%{http_code}' "http://127.0.0.1:$CPA_PORT/v1/chat/completions" \
    -H "Authorization: Bearer test-key" -H "Content-Type: application/json" \
    -d "{\"model\":\"$1\",\"stream\":$2,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"
}
mgmt() { curl -s "$@" -H "Authorization: Bearer mgmt-key"; }

check "virtual model listed with capabilities" '"inputTokenLimit": 200000' \
  "$(curl -s "http://127.0.0.1:$CPA_PORT/v1beta/models?key=test-key" | python3 -m json.tool)"
check "downgrade blocked (non-stream)" "HTTP=503" "$(chat gpt-6-astra false)"
check "flaky downgrade recovered by retry" '"model": "flaky-2026-08-01"' "$(chat flaky false)"
check "virtual fails over quota -> good" '"model": "good-model"' "$(chat smart false)"
check "quota member cooling" '"reason": "quota"' "$(mgmt "http://127.0.0.1:$CPA_PORT/v0/management/plugins/orangeguard/status" | python3 -m json.tool)"
BEFORE="$(grep -c 'UPSTREAM quota-model' "$WORK/upstream.log" || true)"
chat smart false > /dev/null
AFTER="$(grep -c 'UPSTREAM quota-model' "$WORK/upstream.log" || true)"
check "cooling member skipped" "$BEFORE" "$AFTER"
check "cooldown reset" '"cleared"' "$(mgmt -X POST "http://127.0.0.1:$CPA_PORT/v0/management/plugins/orangeguard/cooldown/reset")"
check "downgrade blocked (stream)" "refusing to return a substituted model" "$(chat gpt-6-astra true)"
STREAM="$(chat smart true)"
check "virtual stream served by good-model" '"model": "good-model"' "$STREAM"
if [[ "$STREAM" == *"gpt-5.5-mini"* ]]; then echo "FAIL  substituted stream leaked to client"; FAILED=1; else echo "PASS  no substituted bytes leaked"; fi
check "claude protocol on virtual model" '"model":"good-model"' \
  "$(curl -s "http://127.0.0.1:$CPA_PORT/v1/messages" -H "x-api-key: test-key" -H "anthropic-version: 2023-06-01" \
      -H "Content-Type: application/json" -d '{"model":"smart","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}')"

echo "--- upstream calls"; cat "$WORK/upstream.log"
if [ "$FAILED" -ne 0 ]; then echo "--- cpa log (orangeguard)"; grep -i orangeguard "$WORK/cpa.log" || true; exit 1; fi
echo "all e2e checks passed"
