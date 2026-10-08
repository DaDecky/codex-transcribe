package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"strings"
)

// Browser-style identity used for requests to the private ChatGPT endpoint.
const browserUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36"

const maxUpstreamResponse = 1 << 20

type credentials struct {
	Tokens struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

type upstreamFailure struct {
	status     int
	code       string
	kind       string
	message    string
	retryAfter string
}

func failure(status int, code, kind, message string) *upstreamFailure {
	return &upstreamFailure{status: status, code: code, kind: kind, message: message}
}

func loadCredentials(path string) (credentials, error) {
	var auth credentials
	file, err := os.Open(path)
	if err != nil {
		return auth, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return auth, err
	}
	if len(data) > 64<<10 {
		return auth, errors.New("auth file exceeds 64 KiB")
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		return auth, err
	}
	if strings.TrimSpace(auth.Tokens.AccessToken) == "" {
		return auth, errors.New("ChatGPT access token is missing")
	}
	return auth, nil
}

func (p *proxy) recognize(ctx context.Context, audio io.Reader, header *multipart.FileHeader, language string) (string, *upstreamFailure) {
	auth, err := loadCredentials(p.authFile)
	if err != nil {
		return "", failure(http.StatusServiceUnavailable, "codex_auth_unavailable", "authentication_error", "Cannot load ChatGPT credentials. Run codex login and check the configured auth file.")
	}
	reader, writer := io.Pipe()
	body := multipart.NewWriter(writer)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.upstreamURL, reader)
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return "", failure(http.StatusBadGateway, "upstream_request_failed", "server_error", "Cannot create the transcription request.")
	}
	req.Header.Set("Content-Type", body.FormDataContentType())
	setChatGPTHeaders(req.Header, auth, p.userAgent)
	finished := make(chan error, 1)
	go func() {
		err := writeAudioMultipart(body, audio, header, language)
		_ = writer.CloseWithError(err)
		finished <- err
	}()
	resp, requestErr := p.client.Do(req)
	// A response may arrive before the request body is consumed. Closing the
	// pipe unblocks its writer; wait before the caller closes the audio file.
	_ = reader.Close()
	uploadErr := <-finished
	if requestErr != nil {
		var netErr net.Error
		if errors.Is(requestErr, context.DeadlineExceeded) || (errors.As(requestErr, &netErr) && netErr.Timeout()) {
			return "", failure(http.StatusGatewayTimeout, "upstream_timeout", "server_error", "ChatGPT transcription timed out.")
		}
		return "", failure(http.StatusBadGateway, "upstream_unreachable", "server_error", "Cannot reach ChatGPT transcription.")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", upstreamStatusFailure(resp)
	}
	if uploadErr != nil {
		return "", failure(http.StatusBadGateway, "upstream_upload_failed", "server_error", "Cannot complete the audio upload to ChatGPT.")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamResponse+1))
	if err != nil {
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return "", failure(http.StatusGatewayTimeout, "upstream_timeout", "server_error", "ChatGPT transcription timed out.")
		}
		return "", failure(http.StatusBadGateway, "upstream_response_failed", "server_error", "Cannot read the ChatGPT response.")
	}
	var result struct {
		Text *string `json:"text"`
	}
	if len(data) > maxUpstreamResponse || json.Unmarshal(data, &result) != nil || result.Text == nil {
		return "", failure(http.StatusBadGateway, "invalid_upstream_response", "server_error", "ChatGPT returned an invalid transcription response.")
	}
	return *result.Text, nil
}

func writeAudioMultipart(body *multipart.Writer, audio io.Reader, header *multipart.FileHeader, language string) error {
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": header.Filename}))
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	partHeader.Set("Content-Type", contentType)
	part, err := body.CreatePart(partHeader)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, audio); err != nil {
		return err
	}
	if language != "" && !strings.EqualFold(language, "auto") {
		if err := body.WriteField("language", language); err != nil {
			return err
		}
	}
	return body.Close()
}

func setChatGPTHeaders(header http.Header, auth credentials, userAgent string) {
	if userAgent == "" {
		userAgent = browserUserAgent
	}
	header.Set("Authorization", "Bearer "+auth.Tokens.AccessToken)
	header.Set("User-Agent", userAgent)
	header.Set("Accept", "application/json")
	header.Set("Accept-Language", "en-US,en;q=0.9")
	header.Set("Origin", "https://chatgpt.com")
	header.Set("Referer", "https://chatgpt.com/")
	header.Set("sec-ch-ua", `"Google Chrome";v="149", "Chromium";v="149", "Not_A Brand";v="24"`)
	header.Set("sec-ch-ua-mobile", "?0")
	header.Set("sec-ch-ua-platform", `"macOS"`)
	if auth.Tokens.AccountID != "" {
		header.Set("ChatGPT-Account-ID", auth.Tokens.AccountID)
	}
}

func upstreamStatusFailure(resp *http.Response) *upstreamFailure {
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return failure(http.StatusBadGateway, "codex_auth_rejected", "authentication_error", "ChatGPT rejected the Codex credentials. Refresh the login with Codex and retry.")
	case http.StatusForbidden:
		return failure(http.StatusBadGateway, "upstream_forbidden", "server_error", "ChatGPT denied transcription access. Check the account and browser identity; this private endpoint may have changed.")
	case http.StatusTooManyRequests:
		f := failure(http.StatusTooManyRequests, "upstream_rate_limited", "rate_limit_error", "ChatGPT transcription is rate limited. Retry later.")
		f.retryAfter = resp.Header.Get("Retry-After")
		return f
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return failure(http.StatusBadRequest, "upstream_audio_rejected", "invalid_request_error", "ChatGPT rejected the audio or language hint.")
	default:
		return failure(http.StatusBadGateway, "upstream_error", "server_error", "ChatGPT transcription returned an unexpected HTTP status.")
	}
}
