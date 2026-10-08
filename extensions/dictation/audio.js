import { spawn } from "node:child_process";
import { readFile, stat } from "node:fs/promises";

// SIGINT lets FFmpeg finalize the WAV header; cancellation never uploads it.
export async function record(file, source, signal) {
  signal.throwIfAborted();
  const child = spawn("ffmpeg", [
    "-nostdin", "-hide_banner", "-loglevel", "error",
    "-f", "pulse", "-i", source,
    "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-t", "60", file,
  ], { stdio: ["ignore", "ignore", "pipe"] });
  let diagnostic = "";
  let stopped = false;
  let killTimer;
  const stop = () => {
    if (child.exitCode !== null || child.signalCode !== null || stopped) return;
    stopped = true;
    child.kill(signal.aborted ? "SIGTERM" : "SIGINT");
    killTimer = setTimeout(() => child.kill("SIGKILL"), 1500);
    killTimer.unref();
  };
  signal.addEventListener("abort", stop, { once: true });
  child.stderr.on("data", chunk => { diagnostic = (diagnostic + chunk.toString()).slice(-2048); });
  const limit = setTimeout(stop, 65000);
  limit.unref();
  // Attach rejection handling before waiting for spawn: ENOENT emits both error and close.
  const done = new Promise((resolve, reject) => {
    child.once("error", reject);
    child.once("close", (code, exitSignal) => {
      if (signal.aborted) reject(signal.reason);
      else if (code === 0 || (stopped && code === 255 && !exitSignal)) resolve();
      else reject(new Error(`Recording failed: ${diagnostic.trim() || exitSignal || `FFmpeg exit ${code}`}`));
    });
  }).finally(() => {
    clearTimeout(limit);
    clearTimeout(killTimer);
    signal.removeEventListener("abort", stop);
  });
  // The owner consumes done after spawn; prevent an early rejection becoming unhandled.
  void done.catch(() => {});
  await new Promise((resolve, reject) => {
    child.once("spawn", resolve);
    child.once("error", error => reject(new Error(`Cannot start FFmpeg: ${error.message}. Install FFmpeg with PulseAudio support.`)));
  });
  if (signal.aborted) stop();
  return { stop, done };
}

export async function transcribe(file, endpoint, language, signal, apiKey = "") {
  signal.throwIfAborted();
  if ((await stat(file)).size <= 44) throw new Error("No audio was recorded. Check the selected PulseAudio input.");
  const form = new FormData();
  form.append("file", new Blob([await readFile(file)], { type: "audio/wav" }), "dictation.wav");
  if (language) form.append("language", language);
  const response = await fetch(new URL("/v1/audio/transcriptions", endpoint), {
    method: "POST", body: form, redirect: "error",
    headers: apiKey ? { Authorization: `Bearer ${apiKey}` } : undefined,
    signal: AbortSignal.any([signal, AbortSignal.timeout(95000)]),
  });
  const body = await response.json().catch(() => null);
  if (!response.ok) {
    const code = typeof body?.error?.code === "string" ? ` (${body.error.code})` : "";
    throw new Error(`Transcription failed: HTTP ${response.status}${code}. Check codex-transcribe and your Codex login.`);
  }
  if (typeof body?.text !== "string") throw new Error("The transcription endpoint did not return a text field.");
  const text = body.text.trim();
  if (!text) throw new Error("No speech was recognized. The editor was left unchanged.");
  signal.throwIfAborted();
  return text;
}
