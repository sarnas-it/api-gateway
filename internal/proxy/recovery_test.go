package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestRecoveryMiddleware_AbortHandlerPropagates(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	h := recoveryMiddleware(zap.New(core))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	defer func() {
		rec := recover()
		if rec != http.ErrAbortHandler {
			t.Fatalf("panic = %v, want http.ErrAbortHandler", rec)
		}
		if n := logs.Len(); n != 0 {
			t.Fatalf("abort must not be logged as error: %d entries", n)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	t.Fatal("handler did not panic")
}

func TestRecoveryMiddleware_RealPanicIs500AndLogged(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	h := recoveryMiddleware(zap.New(core))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if logs.Len() != 1 || logs.All()[0].Message != "Panic recovered" {
		t.Fatalf("logs = %+v", logs.All())
	}
}

func TestRecoveryMiddleware_Passthrough(t *testing.T) {
	h := recoveryMiddleware(zap.NewNop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
}
