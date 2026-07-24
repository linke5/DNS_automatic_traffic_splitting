package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSharedDoHServerFallback(t *testing.T) {
	server := &SharedDoHServer{
		entries: map[string]*sharedDoHEntry{
			"/dns-query": {path: "/dns-query"},
		},
	}

	recorder := httptest.NewRecorder()
	server.serveHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status without fallback = %d, want %d", recorder.Code, http.StatusNotFound)
	}

	fallbackCalls := 0
	server.SetFallback(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls++
		w.WriteHeader(http.StatusAccepted)
	}))

	recorder = httptest.NewRecorder()
	server.serveHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("fallback status = %d, want %d", recorder.Code, http.StatusAccepted)
	}
	if fallbackCalls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallbackCalls)
	}

	recorder = httptest.NewRecorder()
	server.serveHTTP(recorder, httptest.NewRequest(http.MethodPut, "/dns-query", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("registered DoH path status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
	if fallbackCalls != 1 {
		t.Fatalf("fallback handled registered DoH path; calls = %d, want 1", fallbackCalls)
	}
}
