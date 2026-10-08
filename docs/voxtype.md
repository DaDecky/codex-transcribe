# Voxtype integration

Use [Voxtype](https://voxtype.io/) on Linux for recording, shortcuts, and text insertion. codex-transcribe provides only the HTTP transcription backend. The full capture/paste recipe was exercised with **Voxtype 1.1.0, codex-transcribe code tagged v0.1.0 built with Go 1.27.1, Linux x86-64, PipeWire, and Hyprland/Wayland**. Use the corrected v0.1.1 release, which aligns the release toolchain with that verification. Other client versions and compositors are not verified here.

Audio is uploaded to ChatGPT through an unofficial private endpoint. A Codex ChatGPT login is required. Neither this recipe nor the proxy guarantees subscription coverage or account eligibility.

## 1. Start the proxy

Follow the [download and checksum instructions](../README.md#quick-start-no-go-toolchain-required). With the proxy stopped, run:

```sh
./codex-transcribe doctor
./codex-transcribe
```

Keep this terminal open. The default endpoint is `http://127.0.0.1:8378`.

## 2. Test without changing your client configuration

Check the installed version with `voxtype --version`. Uploading a file does not record the microphone, but it does send the file to ChatGPT. Supply a WAV that you are comfortable uploading:

```sh
voxtype --whisper-mode remote \
  --remote-endpoint http://127.0.0.1:8378 \
  --remote-model whisper-1 \
  --remote-api-key local-placeholder \
  --language auto \
  transcribe "$HOME/Music/recording.wav"
```

Voxtype 1.1.0 constructs `/v1/audio/transcriptions` from this base URL. Do **not** include that path in `remote_endpoint`. The `whisper-1` value is a compatibility identifier, not a selection of ChatGPT's actual recognition model. The placeholder key is accepted only when the local proxy has no API key configured.

If `CODEX_TRANSCRIBE_API_KEY` is set for the proxy, configure Voxtype with the matching **proxy** key. Never put the Codex access token in Voxtype. Keep any saved key private, and do not include it in screenshots or bug reports.

## 3. Back up and edit only the relevant settings

If you have an existing config, preserve it before editing:

```sh
cp -p "$HOME/.config/voxtype/config.toml" "$HOME/.config/voxtype/config.toml.before-codex-transcribe"
```

Choose a different backup filename if this one already exists. Keep your microphone, hotkey, output, and other preferences. In the existing `[whisper]` section, update/add these keys; do not duplicate the section:

```toml
[whisper]
mode = "remote"
remote_endpoint = "http://127.0.0.1:8378"
remote_model = "whisper-1"
remote_api_key = "local-placeholder"
language = "auto"
```

Select `engine = "whisper"` at the top level if another engine is currently selected. Disable eager/chunked processing for this buffered integration. Preserve your local model configuration if you want to switch back later. If your daemon uses command-line overrides, those may take precedence over the file: inspect `systemctl --user cat voxtype.service` before changing anything.

Leave the existing output behavior intact if it already works. To opt into Voxtype's built-in paste behavior for a **terminal**:

```toml
[output]
mode = "paste"
paste_keys = "ctrl+shift+v"
auto_submit = false
smart_auto_submit = false
```

For GUI text fields the paste key is often `ctrl+v` instead. A fixed paste key does not work universally; clipboard-only `mode = "clipboard"` plus manual paste is the conservative alternative. On Wayland, Voxtype's clipboard/paste behavior requires working tools such as `wl-copy` and `wtype`. Do not enable a second paste mechanism if an existing post-processing hook already pastes text, or you may paste twice.

Restart only your Voxtype daemon after saving. If it is managed by a systemd user service:

```sh
systemctl --user restart voxtype.service
```

Otherwise stop the existing foreground daemon and start `voxtype daemon` again. Do not launch competing daemons. codex-transcribe does not install or modify a background service in this recipe.

## 4. Verify recording and insertion separately

Use an empty, safe input field. Do not test paste into a shell prompt where accidental Enter could execute text. Use your existing recording shortcut, or:

```sh
voxtype record start
# Speak a short sentence that you are comfortable uploading.
voxtype record stop
```

First confirm that transcription succeeds. Then confirm the text appears in the intended field and has not been submitted. If you selected clipboard-only output, paste it manually. Focus changes during transcription can send paste to the wrong application; keep the target stable and avoid sensitive fields.

## Observed verification

The integration was exercised with both file transcription and real daemon capture. For the recorded demo, a public speech sample was played into an isolated virtual audio input; **no microphone was recorded**. Voxtype captured the sample, sent a 16 kHz mono WAV to the proxy, received the actual ChatGPT transcript, and pasted it into a terminal test field using its built-in paste mode. The inserted text was compared against the clipboard result. See the [demo and its limitations](demo.md).

## Troubleshooting and rollback

- **Transcription fails:** first try the file command above. Report the proxy error code, upstream status if present, versions, format, and duration. Never publish credentials or personal audio.
- **`unsupported_parameter`:** your client may send unsupported fields. This is partial OpenAI compatibility; prompt, temperature, timestamps, and streaming are not supported.
- **Text exists but paste fails:** inspect the clipboard, target application's paste shortcut, `wl-copy`/`wtype`, and the client's output configuration. That is separate from HTTP transcription.
- **Paste happens twice:** check for an existing paste hook before enabling built-in paste mode.
- **Daemon keeps using another endpoint:** check command-line overrides and restart it after editing.

To restore the configuration backup (this replaces changes made since that backup):

```sh
cp -p "$HOME/.config/voxtype/config.toml.before-codex-transcribe" "$HOME/.config/voxtype/config.toml"
systemctl --user restart voxtype.service
```

For a foreground daemon, restart it manually instead. Stop codex-transcribe with Ctrl+C. This recipe does not remove your existing models, credentials, shortcuts, or services.
