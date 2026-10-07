package identity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	hmacauth "github.com/X-didgital/go-hmac-auth"

	"github.com/basili4-1982/api-gateway/internal/config"
)

func TestLookupClient_FetchFound(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/api/v1/internal/user-by-email" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("email") != "partner@example.com" {
			t.Errorf("unexpected email %s", r.URL.Query().Get("email"))
		}
		uid := r.Header.Get(hmacauth.UserIDHeader)
		sig := r.Header.Get(hmacauth.SignatureHeader)
		ts := r.Header.Get(hmacauth.TimestampHeader)
		if !hmacauth.VerifyRequest(uid, r.Method, r.URL.Path, ts, sig, "internal-secret", time.Now()) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":7,"email":"partner@example.com","full_name":"Partner","is_active":true,"roles":["viewer"]}}`))
	}))
	defer srv.Close()

	c := newLookupClient(srv.URL, "internal-secret")
	u, err := c.fetch(context.Background(), "partner@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u == nil || u.ID != 7 || u.Email != "partner@example.com" || !u.IsActive {
		t.Fatalf("unexpected user: %+v", u)
	}
	if len(u.Roles) != 1 || u.Roles[0] != "viewer" {
		t.Fatalf("unexpected roles: %+v", u.Roles)
	}
}

func TestLookupClient_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newLookupClient(srv.URL, "internal-secret")
	u, err := c.fetch(context.Background(), "nobody@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u != nil {
		t.Fatalf("expected nil user, got %+v", u)
	}
}

func TestResolver_CacheHitAndNegative(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Query().Get("email") == "found@example.com" {
			_, _ = w.Write([]byte(`{"data":{"id":1,"email":"found@example.com","is_active":true}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	r := newResolver(config0(srv.URL))

	u, found, err := r.byEmail(context.Background(), "found@example.com")
	if err != nil || !found || u.ID != 1 {
		t.Fatalf("first lookup failed: u=%+v found=%v err=%v", u, found, err)
	}
	_, _, _ = r.byEmail(context.Background(), "found@example.com")
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected cache hit (1 call), got %d", atomic.LoadInt32(&calls))
	}

	_, found, err = r.byEmail(context.Background(), "missing@example.com")
	if err != nil || found {
		t.Fatalf("expected negative result, got found=%v err=%v", found, err)
	}
	_, _, _ = r.byEmail(context.Background(), "missing@example.com")
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected negative cache (2 calls), got %d", atomic.LoadInt32(&calls))
	}
}

func config0(serviceURL string) config.IdentityUserLookupConfig {
	return config.IdentityUserLookupConfig{
		ServiceURL: serviceURL,
		HMACSecret: "internal-secret",
		CacheTTL:   time.Minute,
	}
}
