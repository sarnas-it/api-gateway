package proxy

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"code.sarnas.ru/api-gateway/internal/config"
)

func ctxRequest(r *httptest.ResponseRecorder, reqID string, reqBody, respBody []byte, ct string) context.Context {
	c := context.WithValue(context.Background(), ctxKeyRequestIDs, requestIDs{reqID: reqID, traceID: reqID})
	if reqBody != nil {
		c = context.WithValue(c, ctxKeyRequestBody, reqBody)
	}
	if respBody != nil {
		c = context.WithValue(c, ctxKeyResponseBody, respBody)
	}
	if ct != "" {
		c = context.WithValue(c, ctxKeyResponseContentType, ct)
	}
	return c
}

func TestBuildEventBodyToggles(t *testing.T) {
	p := &Publisher{}

	reqBody := json.RawMessage(`{"name":"eq"}`)
	respBody := json.RawMessage(`{"data":{"id":9}}`)

	r := httptest.NewRequest("POST", "/api/equipment", nil)
	r = r.WithContext(ctxRequest(httptest.NewRecorder(), "r1", reqBody, respBody, "application/json"))

	// По умолчанию (include_request_body не задан): тело запроса публикуется,
	// тело ответа — нет.
	wh := config.WebhookConfig{Name: "a", Trigger: config.TriggerOnResponse}
	ev := p.buildEvent(r, 201, wh)
	if len(ev.Changes) == 0 {
		t.Error("request body must be published by default")
	}
	if len(ev.ResponseBody) != 0 {
		t.Error("response body must not be published unless enabled")
	}

	// Явно включён ответ, выключен запрос.
	wh.IncludeResponseBody = true
	falseVal := false
	wh.IncludeRequestBody = &falseVal
	ev = p.buildEvent(r, 201, wh)
	if len(ev.Changes) != 0 {
		t.Error("request body must be hidden when include_request_body=false")
	}
	if string(ev.ResponseBody) != string(respBody) {
		t.Errorf("response body = %s, want %s", ev.ResponseBody, respBody)
	}
}

func TestBuildEventResponseBodySkipsNonJSON(t *testing.T) {
	p := &Publisher{}
	respBody := json.RawMessage(`{"a":1}`)

	r := httptest.NewRequest("GET", "/api/x", nil)
	r = r.WithContext(ctxRequest(httptest.NewRecorder(), "r2", nil, respBody, "application/octet-stream"))

	wh := config.WebhookConfig{Name: "a", Trigger: config.TriggerOnResponse, IncludeResponseBody: true}
	ev := p.buildEvent(r, 200, wh)
	if len(ev.ResponseBody) != 0 {
		t.Error("non-JSON response body must not be published")
	}
}

func TestCaptureResponseBodies(t *testing.T) {
	p := &Publisher{}
	if p.CaptureResponseBodies() {
		t.Error("no response-body webhook -> must not capture")
	}
	p.webhooks = []config.WebhookConfig{
		{Name: "a", Trigger: config.TriggerOnRequest},
		{Name: "b", Trigger: config.TriggerOnResponse, IncludeResponseBody: true},
	}
	if !p.CaptureResponseBodies() {
		t.Error("on_response webhook with include_response_body -> must capture")
	}
	p.webhooks = []config.WebhookConfig{
		{Name: "b", Trigger: config.TriggerOnResponse},
	}
	if p.CaptureResponseBodies() {
		t.Error("on_response webhook without include_response_body -> must not capture")
	}
}

func TestResponseWriterCapture(t *testing.T) {
	rr := httptest.NewRecorder()
	rw := newResponseWriter(rr)
	rw.enableCapture(8)
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(200)
	body := []byte(`{"data":{"id":9},"pad":"1234567890"}`)
	n, _ := rw.Write(body)
	if n != len(body) {
		t.Fatalf("wrote %d bytes, want %d", n, len(body))
	}
	got := rw.CapturedResponseBody()
	if len(got) != 8 {
		t.Fatalf("captured %d bytes, want 8 (cap)", len(got))
	}
	if rw.CapturedContentType() != "application/json" {
		t.Errorf("content type = %q", rw.CapturedContentType())
	}
	// Без capture ничего не собирается.
	rw2 := newResponseWriter(httptest.NewRecorder())
	rw2.Write([]byte("x"))
	if rw2.CapturedResponseBody() != nil {
		t.Error("capture must be disabled by default")
	}
}
