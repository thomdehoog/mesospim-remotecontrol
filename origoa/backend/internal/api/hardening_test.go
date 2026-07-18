package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	for k, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp == "" {
		t.Error("missing Content-Security-Policy")
	}
}

func TestRecoverPanicReturns500(t *testing.T) {
	h := recoverPanic(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/status", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic yielded %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("panic response content-type = %q", ct)
	}
}

func TestCORSOnlyWhenConfigured(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	wrapped := withCORS(inner, "https://app.example.com")
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest("GET", "/api/status", nil))
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("ACAO = %q, want the configured origin", got)
	}
	// A preflight is answered without reaching the inner handler.
	rec = httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest("OPTIONS", "/api/status", nil))
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight = %d, want 204", rec.Code)
	}
}

func TestWebSocketCheckOrigin(t *testing.T) {
	req := func(origin, host string) *http.Request {
		r := httptest.NewRequest("GET", "http://"+host+"/api/ws", nil)
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}
	cases := []struct {
		name       string
		corsOrigin string
		origin     string
		host       string
		want       bool
	}{
		{"same origin", "", "http://localhost:8000", "localhost:8000", true},
		{"cross origin rejected", "", "http://evil.example.com", "localhost:8000", false},
		{"no origin (non-browser)", "", "", "localhost:8000", true},
		{"configured cross origin", "https://app.example.com", "https://app.example.com", "localhost:8000", true},
		{"wildcard allows any", "*", "http://anything.example.com", "localhost:8000", true},
		{"other cross origin still rejected", "https://app.example.com", "http://evil.example.com", "localhost:8000", false},
	}
	for _, c := range cases {
		h := NewHub(c.corsOrigin)
		if got := h.checkOrigin(req(c.origin, c.host)); got != c.want {
			t.Errorf("%s: checkOrigin = %v, want %v", c.name, got, c.want)
		}
	}
}
