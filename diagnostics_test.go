package main

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestUpstreamStatusDiagnostics(t *testing.T) {
	for _, status := range []int{301, 401, 403, 415, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			p := testProxy(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "private upstream body")
			})
			response := serve(p, uploadRequest(t, nil, []byte("audio")))
			var body struct {
				Error apiError `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.UpstreamStatus != status {
				t.Fatalf("upstream status = %d, want %d", body.Error.UpstreamStatus, status)
			}
		})
	}
}

func TestTransportFailureHasNoUpstreamStatus(t *testing.T) {
	p := testProxy(t, successfulUpstream)
	p.upstreamURL = "http://127.0.0.1:0"
	response := serve(p, uploadRequest(t, nil, []byte("audio")))
	assertError(t, response, 502, "upstream_unreachable")
	var body struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, exists := body.Error["upstream_status"]; exists {
		t.Fatal("transport failure fabricated an HTTP status")
	}
}
