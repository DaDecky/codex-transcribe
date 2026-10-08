# codex-transcribe

A standalone Go HTTP proxy for ChatGPT dictation, authenticated with your existing Codex ChatGPT login. Upload an audio file; receive a transcript. No desktop integration, microphone capture, local recognition model, or separate OpenAI Platform API key required.

**Unofficial integration:** this calls ChatGPT's private `/backend-api/transcribe` endpoint. It is not endorsed by OpenAI, may break without notice, and does not guarantee account eligibility, subscription coverage, or pricing. Use your own account and comply with the service's terms. Audio is uploaded to OpenAI.

## Quick start

Requires Go 1.24+ and a Codex ChatGPT login stored in an auth file. API-key-only Codex authentication is not sufficient.

```sh
codex login

git clone https://github.com/DaDecky/codex-transcribe.git
cd codex-transcribe
go build -o codex-transcribe .
./codex-transcribe
```

The listener defaults to `127.0.0.1:8378`. This repository is initially private; cloning requires GitHub access.

```sh
curl --fail-with-body http://127.0.0.1:8378/v1/audio/transcriptions \
  -F 'file=@recording.wav;type=audio/wav' \
  -F 'language=en'
```

```json
{"text":"Your transcribed speech."}
```

For plain text:

```sh
curl --fail-with-body http://127.0.0.1:8378/v1/audio/transcriptions \
  -F 'file=@recording.wav;type=audio/wav' \
  -F 'response_format=text'
```

The proxy does not record audio or paste text. Any HTTP client can use it. The buffered path was smoke-tested against ChatGPT with a public 16 kHz mono WAV speech sample, returning the recognized transcript in both JSON and plain text. Other audio formats depend on the private upstream endpoint and are not guaranteed by this project.

## API

### `POST /v1/audio/transcriptions`

The request must be `multipart/form-data` with a boundary. One file per request.

| Field | Required | Contract |
| --- | --- | --- |
| `file` | Yes | Non-empty audio upload. The maximum **entire multipart request**, including overhead, is 25 MiB by default. |
| `language` | No | Short language hint such as `en` or `id`. Omitted or `auto` leaves detection to ChatGPT. |
| `model` | No | Only `whisper-1` is accepted as a client compatibility identifier. It is not forwarded and does not select the underlying ChatGPT model. |
| `response_format` | No | `json` (default) returns `{"text":"..."}`; `text` returns UTF-8 plain text. |

Unknown fields, duplicate fields/files, other models, and other response formats are rejected with HTTP 400 instead of silently ignored. Parameters are read only from the multipart body. `prompt`, `temperature`, timestamps, diarization, SRT, VTT, and client-facing streaming are not supported. An empty transcript is valid for audio with no recognized speech.

Successful JSON responses contain only `text`; private upstream metadata is not exposed. Every error, including errors for text-format requests, uses this shape:

```json
{
  "error": {
    "message": "This transcription parameter is not supported.",
    "type": "invalid_request_error",
    "code": "unsupported_parameter",
    "param": "prompt"
  }
}
```

`param` is included for field-specific errors and otherwise omitted. Clients should branch on `error.code`, not message wording.

| HTTP status | Example codes | Meaning |
| --- | --- | --- |
| 400 | `invalid_multipart`, `missing_file`, `unsupported_parameter`, `duplicate_parameter`, `unsupported_model`, `unsupported_response_format`, `invalid_language`, `upstream_audio_rejected` | Invalid input, unsupported options, or audio rejected by ChatGPT. |
| 401 | `invalid_api_key` | Missing or incorrect **proxy** bearer token, if configured. |
| 404 / 405 | `not_found`, `method_not_allowed` | Wrong endpoint or method. |
| 413 | `upload_too_large` | Entire multipart request exceeds the configured limit. |
| 429 | `upstream_rate_limited` | ChatGPT rate limit; upstream `Retry-After` is preserved when supplied. |
| 500 | `upload_read_failed` | Local upload storage cannot be read. |
| 502 | `codex_auth_rejected`, `upstream_forbidden`, `upstream_unreachable`, `upstream_request_failed`, `upstream_upload_failed`, `upstream_response_failed`, `invalid_upstream_response`, `upstream_error` | Upstream authentication, transport, or protocol failure. |
| 503 | `codex_auth_unavailable` | Auth file is missing, malformed, too large, or lacks a ChatGPT access token. |
| 504 | `upstream_timeout` | ChatGPT request or response timed out. |

Upstream errors are sanitized. HTML challenge pages, raw upstream bodies, credentials, and asset pointers are not returned to clients. Requests are not automatically retried: callers control whether to repeat an audio submission.

### `GET /healthz`

Returns `{"status":"ok"}`. This is a public process-liveness endpoint, **not** a login validity check or an upstream availability probe. `HEAD` is also supported.

## Configuration

```sh
./codex-transcribe \
  -listen 127.0.0.1:8378 \
  -auth-file "$HOME/.codex/auth.json" \
  -timeout 90s \
  -max-upload-mib 25
```

| Setting | Default / behavior |
| --- | --- |
| `-listen` | `127.0.0.1:8378`. A non-loopback listener requires a proxy API key. |
| `-auth-file` | Explicit flag overrides `CODEX_TRANSCRIBE_AUTH_FILE`, then `$CODEX_HOME/auth.json`, then `$HOME/.codex/auth.json`. |
| `-timeout` | `90s`, greater than zero and at most one hour. Bounds the upstream request. |
| `-max-upload-mib` | `25`, between 1 and 1024 MiB. Bounds the complete multipart request body. |
| `CODEX_TRANSCRIBE_API_KEY` | Unset for local, unauthenticated access. When set, every endpoint except `/healthz` requires `Authorization: Bearer <key>`. |
| `CODEX_TRANSCRIBE_USER_AGENT` | Browser-style default. Override if ChatGPT's private endpoint requires an updated identity. |

Authentication is re-read for each request, so Codex token rotation is picked up without restarting the proxy. **Codex owns login and token refresh**; this proxy does not perform OAuth or write your credentials. If the upstream rejects the token, use Codex to refresh your login and try again. It does not read Codex provider configuration or redirect to a custom chat backend.

For authenticated local access:

```sh
export CODEX_TRANSCRIBE_API_KEY='choose-a-long-random-secret'
./codex-transcribe
```

In another shell with the same key configured:

```sh
curl --fail-with-body http://127.0.0.1:8378/v1/audio/transcriptions \
  -H "Authorization: Bearer $CODEX_TRANSCRIBE_API_KEY" \
  -F 'file=@recording.wav;type=audio/wav'
```

Keep the listener on loopback unless you deliberately deploy it behind authenticated TLS. The built-in server is plain HTTP; bearer tokens do not encrypt traffic. Without a proxy key, other local processes can use your ChatGPT account through this listener. No CORS support is provided for browser integrations.

## Implementation and privacy

- Go standard library only; no runtime package dependencies.
- Uploads over 1 MiB may spill to Go's temporary directory. Multipart temporary files are removed when the request finishes, including failed requests. A crash can leave files behind; use an appropriate `TMPDIR` if disk persistence is a concern.
- The upstream multipart body is streamed from the upload, avoiding another full audio buffer.
- Upstream responses are capped at 1 MiB; auth files are capped at 64 KiB.
- Audio bytes are forwarded unchanged: no loudness normalization, cleanup, or model download.
- Client cancellation cancels the upstream request. HTTP redirects are not followed.
- The proxy does not log audio, transcripts, bearer tokens, or raw upstream errors. OpenAI controls its own retention; this proxy does not impose a remote retention policy.
- SIGINT/SIGTERM initiate graceful shutdown with a 10-second deadline.

## Development

```sh
go test -race ./...
go vet ./...
go build .
```

Tests use local HTTP servers and temporary credentials; they do not contact ChatGPT or require a real account.

## Acknowledgments

Thanks to [sicko7947](https://github.com/sicko7947) for [codex-dictate](https://github.com/sicko7947/codex-dictate). Its browser identity and ChatGPT request headers informed the corresponding implementation here and were adapted under the MIT license.

## License

MIT. Copyright notices for included and adapted code are preserved in [LICENSE](LICENSE).
