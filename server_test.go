package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testProxy(t *testing.T, upstream http.HandlerFunc) *proxy {
	t.Helper()
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "auth.json")
	writeTestAuth(t, path, "test-token")
	return &proxy{authFile: path, maxUpload: 25 << 20, upstreamURL: server.URL, client: server.Client()}
}

func writeTestAuth(t *testing.T, path, token string) {
	t.Helper()
	data := fmt.Sprintf(`{"tokens":{"access_token":%q,"account_id":"test-account"}}`, token)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

type field struct{ name, value string }

func uploadRequest(t *testing.T, fields []field, files ...[]byte) *http.Request {
	t.Helper()
	var data bytes.Buffer
	body := multipart.NewWriter(&data)
	for _, f := range fields {
		if err := body.WriteField(f.name, f.value); err != nil {
			t.Fatal(err)
		}
	}
	for _, audio := range files {
		part, err := body.CreateFormFile("file", "recording.wav")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(audio); err != nil {
			t.Fatal(err)
		}
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(data.Bytes()))
	req.Header.Set("Content-Type", body.FormDataContentType())
	return req
}

func serve(p *proxy, req *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	p.handler().ServeHTTP(response, req)
	return response
}

func assertError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", response.Code, status, response.Body.String())
	}
	var result struct {
		Error apiError `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("invalid error JSON: %v", err)
	}
	if result.Error.Code != code {
		t.Fatalf("error code = %q, want %q", result.Error.Code, code)
	}
}

func successfulUpstream(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"text":"Hello, dunia.","asset_pointer":"private-asset"}`)
}

func TestResponseFormats(t *testing.T) {
	p := testProxy(t, successfulUpstream)
	for _, test := range []struct{ format, contentType, body string }{
		{"json", "application/json", "{\"text\":\"Hello, dunia.\"}\n"},
		{"text", "text/plain; charset=utf-8", "Hello, dunia."},
	} {
		t.Run(test.format, func(t *testing.T) {
			response := serve(p, uploadRequest(t, []field{{"response_format", test.format}}, []byte("audio")))
			if response.Code != http.StatusOK || response.Body.String() != test.body || response.Header().Get("Content-Type") != test.contentType {
				t.Fatalf("unexpected response: %d %s %q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
			}
		})
	}
}

func TestRejectedUploads(t *testing.T) {
	p := testProxy(t, func(_ http.ResponseWriter, _ *http.Request) { t.Error("invalid request reached upstream") })
	for _, test := range []struct {
		name   string
		fields []field
		files  [][]byte
		code   string
	}{
		{"missing", nil, nil, "missing_file"},
		{"empty", nil, [][]byte{{}}, "missing_file"},
		{"duplicate_file", nil, [][]byte{[]byte("a"), []byte("b")}, "duplicate_parameter"},
		{"unsupported_field", []field{{"prompt", "do not silently ignore me"}}, [][]byte{[]byte("a")}, "unsupported_parameter"},
		{"duplicate_language", []field{{"language", "en"}, {"language", "id"}}, [][]byte{[]byte("a")}, "duplicate_parameter"},
		{"unsupported_model", []field{{"model", "gpt-4o-transcribe"}}, [][]byte{[]byte("a")}, "unsupported_model"},
		{"unsupported_format", []field{{"response_format", "srt"}}, [][]byte{[]byte("a")}, "unsupported_response_format"},
		{"invalid_language", []field{{"language", "en\r\nsecret"}}, [][]byte{[]byte("a")}, "invalid_language"},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertError(t, serve(p, uploadRequest(t, test.fields, test.files...)), 400, test.code)
		})
	}
}

func TestMalformedMultipart(t *testing.T) {
	p := testProxy(t, func(_ http.ResponseWriter, _ *http.Request) { t.Error("invalid multipart reached upstream") })
	for _, contentType := range []string{"application/json", "multipart/form-data", "multipart/form-data; boundary=broken"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", strings.NewReader("truncated"))
		req.Header.Set("Content-Type", contentType)
		assertError(t, serve(p, req), 400, "invalid_multipart")
	}
}

func TestUploadLimitBoundary(t *testing.T) {
	p := testProxy(t, successfulUpstream)
	request := uploadRequest(t, nil, []byte("audio"))
	p.maxUpload = request.ContentLength
	if response := serve(p, request); response.Code != 200 {
		t.Fatalf("exact limit rejected: %s", response.Body.String())
	}
	request = uploadRequest(t, nil, []byte("audio"))
	p.maxUpload = request.ContentLength - 1
	assertError(t, serve(p, request), 413, "upload_too_large")
}

func TestTemporaryUploadsAreRemoved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	p := testProxy(t, successfulUpstream)
	response := serve(p, uploadRequest(t, nil, bytes.Repeat([]byte("a"), (1<<20)+1)))
	if response.Code != 200 {
		t.Fatalf("disk-spooled upload failed: %s", response.Body.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "multipart-") {
			t.Fatalf("audio persisted after response: %s", entry.Name())
		}
	}
}

func TestProxyAuthentication(t *testing.T) {
	p := testProxy(t, successfulUpstream)
	p.apiKey = "local-secret"
	for _, auth := range []string{"", "Bearer wrong", "Basic local-secret"} {
		req := uploadRequest(t, nil, []byte("audio"))
		req.Header.Set("Authorization", auth)
		assertError(t, serve(p, req), 401, "invalid_api_key")
	}
	req := uploadRequest(t, nil, []byte("audio"))
	req.Header.Set("Authorization", "Bearer local-secret")
	if response := serve(p, req); response.Code != 200 {
		t.Fatalf("authorized transcription rejected: %s", response.Body.String())
	}
}

func TestCredentialsReloadAfterRotation(t *testing.T) {
	p := testProxy(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.Header.Get("Authorization") != "Bearer refreshed-token" {
			w.WriteHeader(401)
			return
		}
		_, _ = io.WriteString(w, `{"text":"Recovered after rotation."}`)
	})
	assertError(t, serve(p, uploadRequest(t, nil, []byte("audio"))), 502, "codex_auth_rejected")
	writeTestAuth(t, p.authFile, "refreshed-token")
	response := serve(p, uploadRequest(t, nil, []byte("audio")))
	if response.Code != 200 || !strings.Contains(response.Body.String(), "Recovered after rotation.") {
		t.Fatalf("proxy retained stale credentials: %s", response.Body.String())
	}
}

func TestUnavailableCredentials(t *testing.T) {
	p := testProxy(t, func(_ http.ResponseWriter, _ *http.Request) { t.Error("missing credentials reached upstream") })
	for _, data := range []string{`{`, `{}`, `{"tokens":{"access_token":" "}}`} {
		if err := os.WriteFile(p.authFile, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		assertError(t, serve(p, uploadRequest(t, nil, []byte("audio"))), 503, "codex_auth_unavailable")
	}
}

func TestUpstreamFailuresAreSanitized(t *testing.T) {
	for _, test := range []struct {
		upstream, status int
		code             string
	}{
		{401, 502, "codex_auth_rejected"},
		{403, 502, "upstream_forbidden"},
		{429, 429, "upstream_rate_limited"},
		{415, 400, "upstream_audio_rejected"},
		{503, 502, "upstream_error"},
	} {
		t.Run(test.code, func(t *testing.T) {
			p := testProxy(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Retry-After", "17")
				w.WriteHeader(test.upstream)
				_, _ = io.WriteString(w, "upstream-private-detail test-token")
			})
			response := serve(p, uploadRequest(t, nil, []byte("audio")))
			assertError(t, response, test.status, test.code)
			if strings.Contains(response.Body.String(), "upstream-private-detail") || strings.Contains(response.Body.String(), "test-token") {
				t.Fatal("upstream details leaked to client")
			}
			if test.upstream == 429 && response.Header().Get("Retry-After") != "17" {
				t.Fatal("rate limit retry information was lost")
			}
		})
	}
}

func TestUpstreamTranscriptValidation(t *testing.T) {
	for _, body := range []string{`{"text":null}`, `{}`, `<html>challenge</html>`, `{"text":42}`, strings.Repeat("x", maxUpstreamResponse+1)} {
		p := testProxy(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = io.WriteString(w, body)
		})
		assertError(t, serve(p, uploadRequest(t, nil, []byte("audio"))), 502, "invalid_upstream_response")
	}
	p := testProxy(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{"text":""}`)
	})
	response := serve(p, uploadRequest(t, nil, []byte("silence")))
	if response.Code != 200 || response.Body.String() != "{\"text\":\"\"}\n" {
		t.Fatalf("valid silence transcript rejected: %s", response.Body.String())
	}
}

func TestClientCancellationReachesUpstream(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	p := testProxy(t, func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(canceled)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := uploadRequest(t, nil, []byte("audio")).WithContext(ctx)
	done := make(chan struct{})
	go func() { defer close(done); serve(p, req) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream never received the request")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream kept processing canceled audio")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("request writer did not terminate after cancellation")
	}
}

func TestNonLoopbackListenerRequiresAuthentication(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8378", "[::]:8378", "localhost:8378", "192.168.1.20:8378"} {
		if err := validateListen(addr, ""); err == nil {
			t.Fatalf("unauthenticated non-literal-loopback address accepted: %s", addr)
		}
	}
	if err := validateListen("127.0.0.1:8378", ""); err != nil {
		t.Fatal(err)
	}
	if err := validateListen("[::1]:8378", ""); err != nil {
		t.Fatal(err)
	}
	if err := validateListen("0.0.0.0:8378", "configured-secret"); err != nil {
		t.Fatal(err)
	}
}
