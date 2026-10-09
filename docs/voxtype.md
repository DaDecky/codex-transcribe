# Voxtype integration

Use [Voxtype](https://voxtype.io/) on Linux for recording, shortcuts, and text insertion. codex-transcribe provides only the HTTP transcription backend. The full capture/paste recipe was exercised with **Voxtype 1.1.0, codex-transcribe code tagged v0.1.0 built with Go 1.27.1, Linux x86-64, PipeWire, and Hyprland/Wayland**. Use the corrected v0.1.1 release, which aligns the release toolchain with that verification. Other client versions and compositors are not verified here.

Audio is uploaded to ChatGPT through an unofficial private endpoint. A Codex ChatGPT login is required. Neither this recipe nor the proxy guarantees subscription coverage or account eligibility.

## Automatic installer: Linux + systemd user service

With Voxtype already installed and your Codex ChatGPT account logged in (`codex login`), run:

```sh
git clone https://github.com/DaDecky/codex-transcribe.git
cd codex-transcribe
python3 scripts/install-voxtype.py install
```

Requires Python 3.11 or newer, Linux x86-64/ARM64, an existing Voxtype `config.toml` and `voxtype.service` user unit, and a working systemd user session. Set up your normal Voxtype recording/output first; this installer connects that existing client to the backend. No Go compiler, pip packages, root access, or separate proxy terminal. The installer downloads the pinned v0.1.1 backend from GitHub and verifies its published SHA-256 before executing/installing it. An offline credential check confirms readable token presence, not login validity or account access.

The installer installs `~/.local/bin/codex-transcribe`, creates `codex-transcribe.service`, and adds a Voxtype dependency drop-in. It updates only the selected engine and remote-backend settings; existing language, microphone, local model, hotkeys, output mode, paste hook, and auto-submit preferences are preserved. It does not install Voxtype, change compositor shortcuts, record audio, configure paste, or refresh credentials. An existing active Voxtype service is restarted to load the configuration; an inactive daemon is not started automatically.

Before restarting Voxtype, the installer verifies that its own backend process owns the configured loopback listener and passes a bounded health check. This is local readiness, not proof that ChatGPT accepts the login. The backend is enabled under the user manager's `default.target` so it does not depend on a specific compositor's graphical-session integration.

The default loopback endpoint is `http://127.0.0.1:8378`. Use `--port 8377` or another free port if needed. An unrelated listener is never stopped. CLI flags in an existing Voxtype unit can override its config; conflicting overrides must be resolved before installation rather than silently discarded. Existing unrelated drop-ins remain intact.

```sh
python3 scripts/install-voxtype.py install --port 18378
```

Every installation prints a unique private backup directory. To roll back, pass that exact path:

```sh
python3 scripts/install-voxtype.py rollback /absolute/path/to/printed-backup-directory
```

Rollback restores the prior configuration, binary, installer-managed unit/drop-in, and recorded service state. It refuses to overwrite files edited after installation. Keep the backup private; it can contain existing client settings or local proxy keys. A failed installation attempts to restore the prior state and reports any recovery failure instead of claiming success.

For offline preparation or isolated verification, `install --no-start` writes the files without changing the user manager. It still downloads/verifies the binary and checks local credentials. That mode does not enable/start/restart services; use the default install for the normal automatic setup. Standard `HOME`, `XDG_CONFIG_HOME`, and `XDG_STATE_HOME` select installation locations.

Audio will be uploaded to OpenAI only when you subsequently dictate or explicitly transcribe a file. After setup, verify with a consented WAV or a safe input field using the steps below. Auto-submit is preserved, not forcibly disabled: inspect your existing output settings before testing.

Inspect or stop/start the installed backend with:

```sh
systemctl --user status codex-transcribe.service
journalctl --user -u codex-transcribe.service -n 30 --no-pager
systemctl --user stop codex-transcribe.service
systemctl --user start codex-transcribe.service
```

The backend logs no audio, transcripts, OAuth tokens, or raw upstream response bodies. Review local paths/account metadata before sharing diagnostics. Use installer rollback for removal/restoration rather than deleting the unit or binary by hand.

## Manual setup

The following foreground recipe remains available if you do not use the installer.

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

The installer was exercised separately on Linux x86-64 with an isolated home/config/state directory and uniquely named real systemd user units. It downloaded/checksummed the official v0.1.1 binary, passed offline doctor, installed and health-checked its own background process, and configured Voxtype 1.1.0. File transcription of the public JFK sample returned the real ChatGPT transcript in 1.24 seconds. Rollback restored the original config byte-for-byte, removed created binary/unit/drop-in files, and restored the prior enabled/active fixture-daemon state. The CLI `--no-start` install/rollback path was also exercised. Existing production services, microphone, clipboard, and paste hook were not used or changed by these installer checks. Full logout/login, ARM64 installer execution, and macOS installation are not verified; macOS is not supported by this optional installer.

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
