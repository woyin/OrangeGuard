# Mock OpenAI-compatible upstream for the orangeguard end-to-end test.
# gpt-6-astra is always downgraded to gpt-5.5-mini, flaky is downgraded on its
# first call only, quota-model always answers 429 insufficient_quota.
import json, sys, threading
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
counts = {}
lock = threading.Lock()
def served_for(model, n):
    if model == "gpt-6-astra": return "gpt-5.5-mini"            # always downgraded
    if model == "flaky": return "gpt-5.5-mini" if n == 1 else "flaky-2026-08-01"
    return model
class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        if self.path.endswith("/models"):
            ids = ["gpt-6-astra", "flaky", "quota-model", "good-model"]
            self._json(200, {"object": "list", "data": [{"id": i, "object": "model"} for i in ids]})
        else: self._json(404, {})
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
        model = body.get("model")
        with lock:
            counts[model] = counts.get(model, 0) + 1; n = counts[model]
        sys.stderr.write(f"UPSTREAM {model} #{n} stream={body.get('stream')}\n"); sys.stderr.flush()
        if model == "quota-model":
            return self._json(429, {"error": {"type": "insufficient_quota", "code": "insufficient_quota", "message": "You exceeded your current quota, please check your plan and billing details."}})
        served = served_for(model, n)
        if body.get("stream"):
            self.send_response(200); self.send_header("Content-Type", "text/event-stream"); self.end_headers()
            for delta in [{"role": "assistant", "content": ""}, {"content": "hello from " + served}]:
                chunk = {"id": "c1", "object": "chat.completion.chunk", "model": served, "choices": [{"index": 0, "delta": delta, "finish_reason": None}]}
                self.wfile.write(b"data: " + json.dumps(chunk).encode() + b"\n\n"); self.wfile.flush()
            fin = {"id": "c1", "object": "chat.completion.chunk", "model": served, "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]}
            self.wfile.write(b"data: " + json.dumps(fin).encode() + b"\n\ndata: [DONE]\n\n"); self.wfile.flush()
            return
        self._json(200, {"id": "c1", "object": "chat.completion", "model": served,
            "choices": [{"index": 0, "message": {"role": "assistant", "content": "hello from " + served}, "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
    def _json(self, code, obj):
        data = json.dumps(obj).encode()
        self.send_response(code); self.send_header("Content-Type", "application/json"); self.send_header("Content-Length", str(len(data))); self.end_headers(); self.wfile.write(data)
ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1]) if len(sys.argv) > 1 else 18080), H).serve_forever()
