package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/woyin/orangeguard/internal/config"
)

func TestNestedErrorMatrix(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, status := range []int{400, 429, 500} {
			for _, parentAllows := range []bool{false, true} {
				t.Run(fmt.Sprintf("stream=%v/status=%d/parent=%v", stream, status, parentAllows), func(t *testing.T) {
					raw := strings.Replace(nestedConfig, "- name: parent", fmt.Sprintf("- name: parent\n    failover_on_client_error: %v", parentAllows), 1)
					h := newFakeHost(map[string][]outcome{"gpt-a": {{status: status, msg: "injected failure"}}, "gpt-b": {{status: status, msg: "injected failure"}}, "gpt-c": {{served: "gpt-c"}}})
					e := newEngine(t, h, raw)
					var err error
					if stream {
						err = e.ExecuteStream(req("parent"), &sink{})
					} else {
						_, f := e.Execute(req("parent"))
						if f != nil {
							err = f
						}
					}
					wantSuccess := status != 400 || parentAllows
					if (err == nil) != wantSuccess {
						t.Fatalf("err=%v calls=%v", err, h.calls)
					}
					if status == 400 && !parentAllows && len(h.calls) != 1 {
						t.Fatalf("4xx crossed parent boundary: %v", h.calls)
					}
				})
			}
		}
	}
}

type callbackHost struct {
	*fakeHost
	before func()
}

func (h *callbackHost) Execute(r Request, m string, b []byte) (Response, error) {
	if h.before != nil {
		f := h.before
		h.before = nil
		f()
	}
	return h.fakeHost.Execute(r, m, b)
}
func (h *callbackHost) ExecuteStream(r Request, m string, b []byte) (StreamStart, error) {
	if h.before != nil {
		f := h.before
		h.before = nil
		f()
	}
	return h.fakeHost.ExecuteStream(r, m, b)
}
func TestNestedConfigurationSnapshot(t *testing.T) {
	for _, stream := range []bool{false, true} {
		h := &callbackHost{fakeHost: newFakeHost(map[string][]outcome{"gpt-a": {{served: "wrong"}}, "gpt-b": {{served: "gpt-b"}}, "gpt-c": {{served: "gpt-c"}}})}
		old, _, err := config.Parse([]byte(nestedConfig))
		if err != nil {
			t.Fatal(err)
		}
		e := New(h)
		e.SetConfig(old)
		changed, _, err := config.Parse([]byte(strings.ReplaceAll(nestedConfig, "model: gpt-b", "model: gpt-c")))
		if err != nil {
			t.Fatal(err)
		}
		h.before = func() { e.SetConfig(changed) }
		if stream {
			if err := e.ExecuteStream(req("parent"), &sink{}); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, f := e.Execute(req("parent")); f != nil {
				t.Fatal(f)
			}
		}
		if strings.Join(h.calls, ",") != "gpt-a,gpt-b" {
			t.Fatalf("in-flight configuration changed: %v", h.calls)
		}
	}
}

type cancelledHost struct{ *fakeHost }

func (h *cancelledHost) Execute(Request, string, []byte) (Response, error) {
	return Response{}, context.Canceled
}
func (h *cancelledHost) ExecuteStream(Request, string, []byte) (StreamStart, error) {
	return StreamStart{}, context.Canceled
}
func TestNestedCancellationTerminates(t *testing.T) {
	for _, stream := range []bool{false, true} {
		base := newEngine(t, newFakeHost(nil), nestedConfig)
		base.host = &cancelledHost{newFakeHost(nil)}
		var f *Failure
		if stream {
			err := base.ExecuteStream(req("parent"), &sink{})
			f, _ = err.(*Failure)
		} else {
			_, f = base.Execute(req("parent"))
		}
		if f == nil || f.Code != "request_cancelled" {
			t.Fatalf("cancellation retried or lost: %+v", f)
		}
	}
}

func TestAvailabilityDoesNotAdvanceRotation(t *testing.T) {
	e := newEngine(t, newFakeHost(nil), strings.Replace(nestedConfig, "- name: child", "- name: child\n    strategy: round-robin", 1))
	cfg := e.Config()
	vm, _ := cfg.FindVirtual("child")
	for i := 0; i < 10; i++ {
		e.Status()
		e.availability(cfg, "child", nil)
	}
	if got := e.order(vm)[0].Model; got != "gpt-a" {
		t.Fatalf("preview advanced counter: %s", got)
	}
	if got := e.order(vm)[0].Model; got != "gpt-b" {
		t.Fatalf("real execution did not advance: %s", got)
	}
}
