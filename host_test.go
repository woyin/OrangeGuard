package main

import (
	"context"
	"errors"
	"testing"
)

func TestSerializedHostCancellation(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		raw := errorEnvelope("host_call_failed", cause.Error(), 0)
		_, err := decodeHostResponse("host.model.execute", raw, 1)
		adapted := upstreamError(err)
		if !errors.Is(adapted, cause) {
			t.Fatalf("serialized cancellation lost: %s: %v", raw, adapted)
		}
		var wrapper *hostCallError
		if !errors.As(adapted, &wrapper) {
			t.Fatal("original callback evidence lost")
		}
	}
	for _, msg := range []string{"upstream context canceled unexpectedly", "context deadline exceeded while connecting"} {
		_, err := decodeHostResponse("host.model.execute", errorEnvelope("host_call_failed", msg, 502), 1)
		if errors.Is(upstreamError(err), context.Canceled) || errors.Is(upstreamError(err), context.DeadlineExceeded) {
			t.Fatalf("false cancellation: %s", msg)
		}
	}
}
