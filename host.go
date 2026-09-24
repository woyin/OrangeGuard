package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	"github.com/woyin/orangeguard/internal/engine"
)

// cpaHost adapts the cpa host callbacks to engine.Host.
type cpaHost struct{}

type hostModelExecutionRequest struct {
	pluginapi.HostModelExecutionRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type hostLogRequest struct {
	HostCallbackID string         `json:"host_callback_id,omitempty"`
	Level          string         `json:"level,omitempty"`
	Message        string         `json:"message,omitempty"`
	Fields         map[string]any `json:"fields,omitempty"`
}

func (cpaHost) request(req engine.Request, model string, body []byte, stream bool) hostModelExecutionRequest {
	headers := http.Header{}
	for key, values := range req.Headers {
		headers[key] = append([]string(nil), values...)
	}
	headers.Set(bypassHeader, newBypassToken())
	return hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: req.SourceFormat,
			ExitProtocol:  req.SourceFormat,
			Model:         model,
			Stream:        stream,
			Body:          body,
			Headers:       headers,
			Query:         req.Query,
			Alt:           req.Alt,
		},
		HostCallbackID: req.CallbackID,
	}
}

func (h cpaHost) Execute(req engine.Request, model string, body []byte) (engine.Response, error) {
	raw, errCall := callHost(pluginabi.MethodHostModelExecute, h.request(req, model, body, false))
	if errCall != nil {
		return engine.Response{}, upstreamError(errCall)
	}
	var resp pluginapi.HostModelExecutionResponse
	if errDecode := json.Unmarshal(raw, &resp); errDecode != nil {
		return engine.Response{}, errDecode
	}
	return engine.Response{Status: resp.StatusCode, Headers: resp.Headers, Body: resp.Body}, nil
}

func (h cpaHost) ExecuteStream(req engine.Request, model string, body []byte) (engine.StreamStart, error) {
	raw, errCall := callHost(pluginabi.MethodHostModelExecuteStream, h.request(req, model, body, true))
	if errCall != nil {
		return engine.StreamStart{}, upstreamError(errCall)
	}
	var resp pluginapi.HostModelStreamResponse
	if errDecode := json.Unmarshal(raw, &resp); errDecode != nil {
		return engine.StreamStart{}, errDecode
	}
	return engine.StreamStart{ID: resp.StreamID, Status: resp.StatusCode, Headers: resp.Headers}, nil
}

func (cpaHost) ReadStream(id string) (engine.Chunk, error) {
	raw, errCall := callHost(pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{StreamID: id})
	if errCall != nil {
		return engine.Chunk{}, upstreamError(errCall)
	}
	var chunk pluginapi.HostModelStreamReadResponse
	if errDecode := json.Unmarshal(raw, &chunk); errDecode != nil {
		return engine.Chunk{}, errDecode
	}
	return engine.Chunk{Payload: chunk.Payload, Err: chunk.Error, Done: chunk.Done}, nil
}

func (cpaHost) CloseStream(id string) {
	_, _ = callHost(pluginabi.MethodHostModelStreamClose, pluginapi.HostModelStreamCloseRequest{StreamID: id})
}

// Log writes into the cpa log. The host formatter renders only a fixed
// allowlist of fields, so all detail travels inside the message.
func (cpaHost) Log(callbackID, level, message string) {
	_, _ = callHost(pluginabi.MethodHostLog, hostLogRequest{
		HostCallbackID: callbackID,
		Level:          level,
		Message:        message,
	})
}

func upstreamError(err error) error {
	var hostErr *hostCallError
	if errors.As(err, &hostErr) {
		code := hostErr.Code
		if code == "host_call_failed" {
			code = "" // generic wrapper code; the engine picks a meaningful one
		}
		return &engine.UpstreamError{Status: hostErr.HTTPStatus, Code: code, Message: hostErr.Message}
	}
	return err
}

func newBypassToken() string {
	buf := make([]byte, 8)
	if _, errRead := rand.Read(buf); errRead != nil {
		return pluginIdentifier
	}
	return hex.EncodeToString(buf)
}
