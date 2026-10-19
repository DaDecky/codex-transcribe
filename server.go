package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

var activeProxy *proxy

type proxy struct {
	managedKeys bool
	standalone  bool
	authFile    string
	apiKey      string
	maxUpload   int64
	upstreamURL string
	userAgent   string
	client      *http.Client
}

type apiError struct {
	Message        string `json:"message"`
	Type           string `json:"type"`
	Code           string `json:"code"`
	Param          string `json:"param,omitempty"`
	UpstreamStatus int    `json:"upstream_status,omitempty"`
}

func writeError(w http.ResponseWriter, status int, code, kind, message, param string) {
	writeJSON(w, status, struct {
		Error apiError `json:"error"`
	}{apiError{Message: message, Type: kind, Code: code, Param: param}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (p *proxy) handler() http.Handler {
	activeProxy = p
	mux := http.NewServeMux()
	registerDashboard(mux)
	mux.HandleFunc("/accounts/keys", dashboardGuard(requireDashboardAuth(keyAPI)))
	mux.HandleFunc("/accounts/transcriptions", dashboardGuard(requireDashboardAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		p.transcribe(w, r)
	})))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, struct {
			Status string `json:"status"`
		}{"ok"})
	})
	mux.HandleFunc("POST /v1/audio/transcriptions", p.transcribe)
	// Register the path separately so wrong methods also use the JSON error contract.
	mux.HandleFunc("/v1/audio/transcriptions", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "invalid_request_error", "Use POST for transcription.", "")
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "invalid_request_error", "Use GET for health checks.", "")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && r.Method == http.MethodGet {
			http.Redirect(w, r, "/accounts", http.StatusSeeOther)
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "invalid_request_error", "Unknown endpoint.", "")
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Consumer credentials apply only to the public transcription endpoint.
		// Dashboard routes use their own password session and origin checks.
		if r.URL.Path == "/v1/audio/transcriptions" && !p.authorizeConsumer(w, r) {
			return
		}

		mux.ServeHTTP(w, r)
	})
}

func (p *proxy) transcribe(w http.ResponseWriter, r *http.Request) {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		writeError(w, http.StatusBadRequest, "invalid_multipart", "invalid_request_error", "Expected multipart/form-data with a boundary.", "")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, p.maxUpload)
	defer r.Body.Close()
	err = r.ParseMultipartForm(1 << 20)
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "upload_too_large", "invalid_request_error", "The multipart request exceeds the configured upload limit.", "file")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_multipart", "invalid_request_error", "Cannot parse multipart upload.", "")
		}
		return
	}
	form := r.MultipartForm
	for name, values := range form.Value {
		switch name {
		case "model", "language", "response_format":
		default:
			writeError(w, http.StatusBadRequest, "unsupported_parameter", "invalid_request_error", "This transcription parameter is not supported.", name)
			return
		}
		if len(values) != 1 {
			writeError(w, http.StatusBadRequest, "duplicate_parameter", "invalid_request_error", "Supply each parameter exactly once.", name)
			return
		}
	}
	for name, files := range form.File {
		if name != "file" {
			writeError(w, http.StatusBadRequest, "unsupported_parameter", "invalid_request_error", "Only the file upload field is supported.", name)
			return
		}
		if len(files) != 1 {
			writeError(w, http.StatusBadRequest, "duplicate_parameter", "invalid_request_error", "Supply exactly one audio file.", name)
			return
		}
	}
	files := form.File["file"]
	if len(files) != 1 || files[0].Size == 0 {
		writeError(w, http.StatusBadRequest, "missing_file", "invalid_request_error", "A non-empty audio file is required.", "file")
		return
	}
	model := r.PostForm.Get("model")
	if model != "" && model != "whisper-1" {
		writeError(w, http.StatusBadRequest, "unsupported_model", "invalid_request_error", "Only whisper-1 is accepted as a compatibility identifier; the ChatGPT backend selects the recognition model.", "model")
		return
	}
	format := r.PostForm.Get("response_format")
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "text" {
		writeError(w, http.StatusBadRequest, "unsupported_response_format", "invalid_request_error", "Supported response formats are json and text.", "response_format")
		return
	}
	language := r.PostForm.Get("language")
	if len(language) > 32 || strings.ContainsAny(language, "\r\n\x00") {
		writeError(w, http.StatusBadRequest, "invalid_language", "invalid_request_error", "Language must be a short language hint, such as en, id, or auto.", "language")
		return
	}
	fileHeader := files[0]
	testFile, err := fileHeader.Open()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "upload_read_failed", "server_error", "Cannot read the uploaded audio.", "file")
		return
	}
	testFile.Close()

	openAudio := func() (io.ReadCloser, error) {
		return fileHeader.Open()
	}
	text, failure := p.recognize(r.Context(), openAudio, fileHeader, language)
	if failure != nil {
		if retryAfter := failure.retryAfter; retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		writeJSON(w, failure.status, struct {
			Error apiError `json:"error"`
		}{apiError{Message: failure.message, Type: failure.kind, Code: failure.code, UpstreamStatus: failure.upstreamStatus}})
		return
	}
	if format == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(text))
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Text string `json:"text"`
	}{text})
}

func (p *proxy) testConnection(ctx context.Context, acc *account) (bool, int64, string) {
	start := time.Now()
	s := acc.Session
	if s.RefreshToken == "" {
		errStr := "No refresh token; sign-in needed"
		_ = updateAccountTestResult(acc.ID, false, 0, errStr)
		return false, 0, errStr
	}
	if s.ExpiresAt <= time.Now().Add(time.Minute).Unix() {
		next, err := exchangeTokens(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s.RefreshToken}, "client_id": {dictateClientID}}, s)
		if err != nil {
			errStr := fmt.Sprintf("Token renewal failed: %v", err)
			latency := time.Since(start).Milliseconds()
			_ = updateAccountTestResult(acc.ID, false, latency, errStr)
			return false, latency, errStr
		}
		_ = updateAccountSession(acc.ID, next)
		s = next
		acc.Session = next
	}

	var auth credentials
	auth.Tokens.AccessToken = s.AccessToken
	auth.Tokens.AccountID = s.AccountID

	wavData := minimalTestWAV()
	header := &multipart.FileHeader{
		Filename: "test.wav",
		Size:     int64(len(wavData)),
		Header:   make(textproto.MIMEHeader),
	}
	header.Header.Set("Content-Type", "audio/wav")

	testCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	_, fail := p.recognizeSingle(testCtx, auth, bytes.NewReader(wavData), header, "en")
	latency := time.Since(start).Milliseconds()
	if fail != nil {
		errStr := fail.message
		_ = updateAccountTestResult(acc.ID, false, latency, errStr)
		return false, latency, errStr
	}
	_ = updateAccountTestResult(acc.ID, true, latency, "")
	return true, latency, ""
}
