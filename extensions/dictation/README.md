# codex-transcribe dictation for Oh My Pi

An independently installable Oh My Pi extension. Record speech from the terminal, transcribe it through codex-transcribe, and append the result to the native composer. **It never submits a message.** No Voxtype, clipboard, local recognition model, or extension runtime dependencies.

## Requirements

- Linux with PulseAudio or PipeWire's PulseAudio compatibility server.
- FFmpeg with the `pulse` input device (`ffmpeg -devices`). On Arch: `sudo pacman -S ffmpeg`; on Debian/Ubuntu: `sudo apt install ffmpeg`.
- Oh My Pi. The integration is verified with omp 18.8.6; upstream Pi, macOS, RPC, and noninteractive modes are not supported by this package.
- A running [codex-transcribe v0.1.1 or newer](https://github.com/DaDecky/codex-transcribe#quick-start-no-go-toolchain-required), using your own Codex ChatGPT login.

## Install

Start the proxy in its own terminal:

```sh
codex-transcribe doctor
codex-transcribe
```

If you downloaded the binary without adding it to `PATH`, use `./codex-transcribe` instead. The default endpoint is `http://127.0.0.1:8378`. The extension does not launch the proxy, read your Codex credentials, or change other dictation clients.

Clone the repository and link just the extension package:

```sh
git clone https://github.com/DaDecky/codex-transcribe.git
cd codex-transcribe
omp install ./extensions/dictation
```

If you already have the repository, update that checkout and run the last command. Restart omp to load the installed extension. Keep the checkout: a local link points to this directory rather than copying it.

For a one-session trial without installing:

```sh
omp --extension ./extensions/dictation
```

The package can also be distributed as an npm-format archive. Extract it, then `omp install /absolute/path/to/package`; the extracted directory must remain present. No npm registry account or package build is required.

## Use

- **`/dictate`** starts recording; run it again to stop and upload.
- **Ctrl+Alt+D** performs the same toggle without consuming an existing draft. If your terminal cannot distinguish this chord, use `/dictate`.
- **`/dictate cancel`** discards recording or cancels an in-flight transcription.
- Recording stops and transcribes automatically at the **60-second audio limit**. A wall-clock guard stops a stalled recording after 65 seconds.
- The footer shows recording/transcribing state. Other input remains usable.
- The transcript is appended to the **current** draft, including text you type during transcription. Review/edit, then press Enter yourself.
- Switching sessions, branching, navigating the session tree, or shutting down cancels pending dictation. Returning to a session does not resurrect the discarded transcript.

This uses a start/stop toggle, not hold-to-talk: terminal shortcuts do not reliably provide key-release events. The built-in Oh My Pi speech-to-text settings and shortcuts are left unchanged.

## Configuration

Set these environment variables before starting omp:

| Variable | Default | Purpose |
| --- | --- | --- |
| `CODEX_DICTATION_ENDPOINT` | `http://127.0.0.1:8378` | Proxy origin; the extension posts to `/v1/audio/transcriptions` |
| `CODEX_DICTATION_INPUT` | `default` | PulseAudio input name; list inputs with `pactl list short sources` |
| `CODEX_DICTATION_LANGUAGE` | Unset | Optional language hint, e.g. `en` or `pl` |
| `CODEX_DICTATION_API_KEY` | Unset | Optional local proxy bearer key, matching its `CODEX_TRANSCRIBE_API_KEY` |

Example with a proxy on another local port:

```sh
CODEX_DICTATION_ENDPOINT=http://127.0.0.1:18479 CODEX_DICTATION_LANGUAGE=en omp
```

No authentication header is needed for the default unauthenticated loopback proxy. Do not put your Codex OAuth token in the extension configuration. Use HTTPS if explicitly choosing a non-local endpoint; audio goes to the endpoint you configure. HTTP redirects are refused.

## Privacy and troubleshooting

Recording occurs only after an explicit command or shortcut. Mono 16 kHz PCM WAV audio is saved in a private temporary directory and removed after success, failure, or cancellation. A crash or forced process kill can leave that directory behind (`codex-dictation-*` under your system temporary directory).

Audio is uploaded to the proxy when recording finishes, then to OpenAI's unofficial ChatGPT transcription endpoint. Cancelling a request cannot retract audio already uploaded. OpenAI controls its own retention. The extension does not log audio, transcripts, or credentials, and does not write transcripts into conversation history until **you** submit the composer.

- **Cannot start FFmpeg:** install FFmpeg and ensure `ffmpeg` is on the PATH used by omp.
- **Recording failed:** check the PulseAudio compatibility server and selected input. Selecting a monitor records system audio rather than your microphone.
- **HTTP 401:** check the optional local proxy key. **HTTP 502:** check proxy diagnostics and your Codex login; private upstream access may have changed.
- **No speech recognized:** the draft is left unchanged; check your input device and try again.
- **Busy:** wait for transcription or use `/dictate cancel` before starting again.

Uninstall with `omp plugin uninstall codex-transcribe-dictation`, then restart omp. This does not uninstall the proxy or touch Voxtype.

## Development

```sh
cd extensions/dictation
npm test
npm pack
```

Tests exercise invalid/empty transcripts, safe error reporting, and cancellation against local HTTP servers. They do not use a microphone or contact OpenAI. End-to-end verification uses a dedicated virtual PulseAudio input and a public speech sample, with the real extension loaded in an interactive omp session.

Verified on Linux with omp 18.8.6 and the downloaded codex-transcribe v0.1.1 binary: `/dictate` captured the public JFK sample through a dedicated virtual PulseAudio monitor; Ctrl+Alt+D stopped capture; real ChatGPT transcription was appended below an existing draft without submission. In the actual TUI, cancelling capture made no upload, switching sessions cancelled an in-flight request and discarded a late response, and an HTTP 502 left the existing draft intact without displaying the raw error body. Temporary recording directories were removed after every exercised outcome. No real microphone, clipboard, default audio-device selection, or existing Voxtype configuration was used or changed.

MIT; see LICENSE.
