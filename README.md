# codex-transcribe

## Consumer API keys

In the unlocked dashboard, use **API keys → Create key**, naming the consumer.
Copy the `sk-dictate-…` value before dismissing it: it is shown once and is never
stored as plaintext on the server. The list shows only the name, prefix, and
creation date. **Revoke** immediately prevents new requests with that key on
all processes sharing the config. An already-authorized request can finish.
Keys are persisted as SHA-256 hashes in the protected dashboard config. They
use 256 bits of generated randomness, remain valid until revoked, and authorize
transcription using the currently active ChatGPT account (not a fixed account).

All server modes require a valid Bearer key for the consumer endpoint:

```sh
curl http://127.0.0.1:8377/v1/audio/transcriptions \
  -H "Authorization: Bearer $DICTATE_API_KEY" \
  -F file=@recording.wav -F model=whisper-1
```

The base URL for compatible clients is `http://127.0.0.1:8377/v1`. These are
local Dictate keys, not OpenAI Platform keys. A missing/invalid/revoked key
returns an OpenAI-shaped HTTP 401 error. Revoking all managed keys keeps the
endpoint locked. The existing `CODEX_TRANSCRIBE_API_KEY`, when configured,
remains an additional consumer key; it cannot be revoked from the dashboard.

Dashboard recording tests use a separate, password-session-protected local
route (`/accounts/transcriptions`) and need no consumer key in the browser.
Neither API keys nor dashboard cookies can substitute for the other. Health
checks remain public. Existing Voxtype clients must set `remote_api_key` to a
valid consumer key. Do not expose this loopback HTTP service directly online.

## Dashboard password

On first use, open `/accounts` and set a local password (at least 4 characters).
No username is needed. Account management and its API stay locked until sign-in.
The dashboard uses an eight-hour, HMAC-authenticated session cookie with HttpOnly,
SameSite=Strict, and a path restricted to `/accounts`. It runs on HTTP loopback;
keep it local. Existing origin checks and cross-site request protections remain.
Five failed password attempts trigger a one-minute cooldown per server process.

Configuration is created automatically at
`~/.config/codex-dictate/dashboard.json` (or under `$XDG_CONFIG_HOME`). It stores
a random 256-bit session secret plus a salted PBKDF2-SHA256 password hash
(600,000 iterations), never the password itself. The file is mode 0600 and writes
are atomic. The secret persists through restarts; no JWT dependency is needed.
**Lock dashboard** rotates the secret and invalidates all existing dashboard
sessions on every port sharing that config. Account credentials remain in the
OS keyring. The transcription endpoint used by Voxtype requires its own consumer API key
and works while account management is locked.

Initial setup is available only on the local loopback dashboard. Set the password
before leaving the server unattended. There is no web password-reset bypass.

## Standalone account dashboard (Linux, experimental)

The dashboard is in English. **Test dictation** records up to 60 seconds after
you grant browser microphone permission, converts the clip to mono PCM16 WAV at
16 kHz, and lets you play it back. Clicking **Test transcription** explicitly
uploads it using the active account. The result shows transcript, HTTP status,
upstream status on errors, and elapsed time. Recordings stay in page memory and
are discarded when the page closes; tests do not type into the desktop or save
audio to disk.

The default credential source is now the OS Secret Service keyring. Codex CLI,
Codex desktop, and `auth.json` are not needed for this mode. Install `secret-tool`
(libsecret) and enable/unlock your desktop keyring.

```sh
go build -o codex-transcribe .
./codex-transcribe -listen 127.0.0.1:8378
# Open http://127.0.0.1:8378/accounts (the root redirects here too).
```

The minimal dashboard lets you add ChatGPT accounts through browser sign-in,
rename labels, choose the active account, and remove accounts locally. New
accounts become active; removing the active account selects the next saved one.
Accounts receive numbered initial labels; rename them for recognition. It displays
local token-expiry status, not a live entitlement or transcription health check.
Tokens remain in the keyring and are never returned to the browser.

Login belongs to the local server, so the same pending attempt is visible from
any browser on this machine. **Copy link** lets you open its authorization URL
in another browser. If the automatic redirect does not reach the callback,
paste the entire `http://127.0.0.1:1455/auth/callback?...` address into the
callback field and click **Complete sign-in**. Pasted URLs are parsed locally,
never fetched; only the exact loopback callback address and matching state are
accepted. Manual and automatic callbacks share the same single-use guard.
The input is cleared after submission and never persisted to browser storage.
**Cancel** invalidates that attempt and closes its listener so a fresh flow
can start immediately. Old callbacks cannot complete a newer attempt.

This is an **experimental Codex-compatible OAuth flow using the public Codex
client ID**, not an independently registered Dictate client. OpenAI may identify
it as Codex during sign-in. It uses PKCE, validates callback state, and listens
only on `127.0.0.1:1455` for up to five minutes. Only one login runs at a time.
A successful sign-in does not prove the internal transcription endpoint accepts
the new token; verify with actual dictation after login. Official Sign in with
ChatGPT currently excludes audio and transcription:
https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations

Refresh runs before expiry and is serialized across requests/processes so
rotating refresh tokens cannot race. A locked keyring or failed renewal stops
requests; no plaintext credential fallback or implicit account switching is
used. Keyring storage uses the `application=codex-dictate, session=chatgpt` item.
The config directory contains only a non-secret coordination lock.

Optional CLI: `codex-transcribe auth login`, `auth status`, and `auth logout`.
Logout/removal clears **local** credentials only, not the remote OpenAI session.
An in-flight transcription may still finish with the previously active account.
Accounts are not automatically pooled or rotated on errors.

Legacy file mode remains explicit: add `-auth-mode codex` to server/doctor
commands below to use the previous `-auth-file`/`CODEX_HOME` behavior. It relies
on Codex for renewal. The dashboard displays a legacy-mode warning. A configured
`CODEX_TRANSCRIBE_API_KEY` is accepted only for consumer transcription; the
dashboard always uses its own password session. Open the dashboard through the literal IPv4 loopback address.


A standalone Go HTTP proxy for ChatGPT dictation, authenticated with your existing Codex ChatGPT login. Upload an audio file; receive a transcript. No desktop integration, microphone capture, local recognition model, or separate OpenAI Platform API key required.

**Unofficial integration:** this calls ChatGPT's private `/backend-api/transcribe` endpoint. It is not endorsed by OpenAI, may break without notice, and does not guarantee account eligibility, subscription coverage, or pricing. Use your own account and comply with the service's terms. Audio is uploaded to OpenAI.

## Quick start: no Go toolchain required

Linux and macOS binaries are available for x86-64 and ARM64. You need a [Codex ChatGPT login](https://developers.openai.com/codex/auth/), not an API-key-only login. The proxy uses its auth file; it does not perform login or refresh tokens itself.

### 1. Download and verify

In an empty working directory, select your platform and download the versioned archive and checksum:

```sh
version=v0.1.1
case "$(uname -s)" in Linux) platform=linux ;; Darwin) platform=darwin ;; *) echo "Unsupported OS"; exit 1 ;; esac
case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) echo "Unsupported architecture"; exit 1 ;; esac
archive="codex-transcribe_${version}_${platform}_${arch}.tar.gz"
base="https://github.com/DaDecky/codex-transcribe/releases/download/$version"
curl -fLO "$base/$archive"
curl -fLO "$base/$archive.sha256"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum -c "$archive.sha256"
else
  shasum -a 256 -c "$archive.sha256"
fi
```

**Continue only if checksum verification succeeds.** Extract and inspect the version:

```sh
tar -xzf "$archive"
./codex-transcribe -version
```

Checksums detect corrupted downloads; they are not signed provenance attestations. macOS binaries are not code-signed or notarized. If Gatekeeper blocks execution, review the source and release before approving it in your OS security settings; do not disable Gatekeeper globally.

### 2. Check your setup, then start

If you have not signed in to Codex, run `codex login` first. With the proxy stopped:

```sh
./codex-transcribe doctor
./codex-transcribe
```

`doctor` is offline: it checks token presence and whether the listener can bind. It does **not** prove that ChatGPT accepts the token. An occupied port is expected if you already have the proxy running. Keep the server terminal open; press Ctrl+C to stop it.

### 3. Send a recording

In another terminal, use a real audio path (not a placeholder). For example, if your WAV is saved in Music:

```sh
curl --fail-with-body http://127.0.0.1:8378/v1/audio/transcriptions \
  -F "file=@$HOME/Music/recording.wav;type=audio/wav" \
  -F 'language=auto'
```

```json
{"text":"Your transcribed speech."}
```

Add `-F 'response_format=text'` for plain text. This uploads the recording to ChatGPT. A 16 kHz mono PCM16 WAV is verified; direct Ogg/Opus failed in a reported request and remains [under investigation](https://github.com/DaDecky/codex-transcribe/issues/1). Conversion workaround (requires ffmpeg):

```sh
ffmpeg -i "$HOME/Music/recording.opus" -ac 1 -ar 16000 -c:a pcm_s16le "$HOME/Music/recording.wav"
```

For desktop dictation, use the [Voxtype installer](docs/voxtype.md#automatic-installer-linux--systemd-user-service) to set up a verified binary and background service without a second terminal. Voxtype owns recording and text insertion; this proxy only transcribes. The [manual integration recipe](docs/voxtype.md#manual-setup), [real integration demo](docs/demo.md), [troubleshooting](#troubleshooting), and [configuration](#configuration) remain available.

## Native Oh My Pi dictation

Native recording and composer integration live in the separate [omp-dictation](https://github.com/DaDecky/omp-dictation) product. It manages this backend automatically; users do not need a second terminal. This repository stays focused on authentication, transcription, and the HTTP API.

The former `extensions/dictation` package has moved. Follow the new repository's installation/migration guide rather than installing an extension from this checkout.

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

Upstream failures additionally include numeric `error.upstream_status` when an HTTP response was received. It is omitted for local validation, missing credentials, and transport failures without a response. For example, an upstream HTTP 503 is normalized to proxy HTTP 502:

```json
{"error":{"message":"ChatGPT transcription returned an unexpected HTTP status.","type":"server_error","code":"upstream_error","upstream_status":503}}
```


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

`./codex-transcribe doctor -auth-file /path/to/auth.json -listen 127.0.0.1:8378` performs only local checks and exits nonzero if any check fails. `./codex-transcribe -version` prints the embedded version without requiring credentials.


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

## Troubleshooting

- **`doctor` cannot find/read credentials:** sign in with Codex, or select the correct file with `-auth-file`. Never paste auth-file contents into an issue.
- **Port already occupied:** stop your existing proxy or choose another `-listen` port and update your client endpoint.
- **`codex_auth_rejected`:** token presence does not imply validity. Let Codex refresh the login and retry; this proxy never rotates credentials itself.
- **`upstream_forbidden`:** account access or the private endpoint/browser identity may have changed.
- **`upstream_error`:** report `error.code`, `error.upstream_status`, proxy version, format, and duration. Do not attach private recordings, tokens, or raw auth data.
- **`unsupported_parameter`:** the client sent an option outside our [API contract](#api). Disable it in the client; full OpenAI transcription compatibility is not claimed.
- **Transcription succeeded but no text appeared:** that is a client clipboard/paste issue, separate from this proxy. See [Voxtype troubleshooting and rollback](docs/voxtype.md#troubleshooting-and-rollback).

Background-service setup for Voxtype is available through `python3 scripts/install-voxtype.py install`; see the [installation and rollback guide](docs/voxtype.md#automatic-installer-linux--systemd-user-service). The agent integration skill remains tracked in [#6](https://github.com/DaDecky/codex-transcribe/issues/6).

## Development

```sh
go test -race ./...
go vet ./...
go build .
```

Tests use local HTTP servers and temporary credentials; they do not contact ChatGPT or require a real account.

Build from source with Go 1.27.1 or newer. Go 1.27.1 is the authenticated-tested release toolchain; older Go 1.24-built assets returned upstream HTTP 403 in a same-host comparison. The exact upstream rejection mechanism is not established.

```sh
git clone https://github.com/DaDecky/codex-transcribe.git
cd codex-transcribe
go build -o codex-transcribe .
```

Maintainers can package the same four release archives with `bash scripts/release.sh v0.1.1 dist`. Tagged `v*` releases run tests and native startup checks in GitHub Actions before publishing assets. A cross-compiled binary alone is not evidence of native execution or authenticated transcription on that platform. Authenticate and transcribe a consented sample from the actual downloaded asset before recommending a new toolchain.


## Acknowledgments

Thanks to [sicko7947](https://github.com/sicko7947) for [codex-dictate](https://github.com/sicko7947/codex-dictate). Its browser identity and ChatGPT request headers informed the corresponding implementation here and were adapted under the MIT license.

## License

MIT. Copyright notices for included and adapted code are preserved in [LICENSE](LICENSE).
