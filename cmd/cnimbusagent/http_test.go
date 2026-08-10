package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// T104: httpFetch/doRequest previously buffered a response with a bare
// io.ReadAll and no size limit -- the 5s/10s client timeouts bound
// duration, not volume, so a misconfigured or hostile AGENT endpoint
// could still deliver hundreds of MB within the timeout window over a
// virtio-net link, OOM-killing something in a small guest.
func TestHTTPFetchRejectsOversizedResponse(t *testing.T) {
	body := strings.Repeat("x", maxAgentResponseBytes+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	_, err := httpFetch(srv.URL, nil)()
	if err == nil {
		t.Fatal("expected an error for a response exceeding maxAgentResponseBytes, got none")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("expected the error to name the size limit, got: %v", err)
	}
}

func TestHTTPFetchAllowsResponseAtExactlyTheLimit(t *testing.T) {
	body := strings.Repeat("x", maxAgentResponseBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	data, err := httpFetch(srv.URL, nil)()
	if err != nil {
		t.Fatalf("expected a response at exactly the limit to succeed, got: %v", err)
	}
	if len(data) != maxAgentResponseBytes {
		t.Errorf("data length = %d, want %d", len(data), maxAgentResponseBytes)
	}
}

func TestHTTPFetchAllowsOrdinarySmallResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"key":"value"}`))
	}))
	defer srv.Close()

	data, err := httpFetch(srv.URL, nil)()
	if err != nil {
		t.Fatalf("httpFetch: %v", err)
	}
	if string(data) != `{"key":"value"}` {
		t.Errorf("data = %q", data)
	}
}
