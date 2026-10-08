import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { record, transcribe } from "./audio.js";

/** @param {import('@oh-my-pi/pi-coding-agent').ExtensionAPI} pi */
export default function dictation(pi) {
  // Factory-local state: child sessions must never share a microphone or draft.
  let active;

  function cancel() {
    const operation = active;
    if (!operation) return;
    active = undefined;
    operation.controller.abort();
    operation.ctx.ui.setStatus("codex-dictation", undefined);
    return operation.finished;
  }

  async function run(operation) {
    const { ctx, controller } = operation;
    let directory;
    try {
      directory = await mkdtemp(join(tmpdir(), "codex-dictation-"));
      controller.signal.throwIfAborted();
      const file = join(directory, "audio.wav");
      const recording = await record(file, process.env.CODEX_DICTATION_INPUT || "default", controller.signal);
      operation.stop = recording.stop;
      operation.phase = "recording";
      ctx.ui.setStatus("codex-dictation", "Recording · /dictate to stop · /dictate cancel to discard");
      ctx.ui.notify("Dictation recording (60-second limit). Run /dictate or press Ctrl+Alt+D to stop and transcribe.", "info");
      await recording.done;
      controller.signal.throwIfAborted();
      operation.phase = "transcribing";
      ctx.ui.setStatus("codex-dictation", "Transcribing · /dictate cancel to discard");
      const text = await transcribe(
        file, process.env.CODEX_DICTATION_ENDPOINT || "http://127.0.0.1:8378",
        process.env.CODEX_DICTATION_LANGUAGE || "", controller.signal, process.env.CODEX_DICTATION_API_KEY || "",
      );
      // Check immediately before mutation, including sessions switched away and back.
      if (active !== operation || ctx.sessionManager.getSessionId() !== operation.sessionId) return;
      const draft = ctx.ui.getEditorText();
      ctx.ui.setEditorText(draft + (draft && !/\s$/.test(draft) ? "\n" : "") + text);
      ctx.ui.notify("Dictation inserted into the editor. Review it, then press Enter to send.", "info");
    } catch (error) {
      if (!controller.signal.aborted && active === operation) {
        ctx.ui.notify(`Dictation: ${error instanceof Error ? error.message : String(error)}`, "error");
      }
    } finally {
      if (directory) {
        try { await rm(directory, { recursive: true, force: true }); }
        catch { ctx.ui.notify(`Could not remove temporary dictation audio: ${directory}`, "warning"); }
      }
      if (active === operation) {
        active = undefined;
        ctx.ui.setStatus("codex-dictation", undefined);
      }
    }
  }

  function toggle(ctx) {
    if (ctx.mode !== "tui" || !ctx.hasUI || ctx.agent.kind !== "main") {
      ctx.ui.notify("Dictation requires an interactive Oh My Pi terminal session.", "error");
      return;
    }
    if (active) {
      if (active.phase === "recording") {
        active.phase = "stopping";
        active.stop();
        ctx.ui.setStatus("codex-dictation", "Stopping recording…");
      } else {
        ctx.ui.notify("Dictation is busy. Use /dictate cancel to discard it.", "info");
      }
      return;
    }
    if (process.platform !== "linux") {
      ctx.ui.notify("This dictation extension currently supports Linux with PulseAudio or PipeWire-Pulse.", "error");
      return;
    }
    const operation = {
      ctx, sessionId: ctx.sessionManager.getSessionId(),
      controller: new AbortController(), phase: "starting", stop: undefined, finished: undefined,
    };
    active = operation;
    ctx.ui.setStatus("codex-dictation", "Starting microphone…");
    // Commands must return promptly; run owns all errors and temporary-file cleanup.
    operation.finished = run(operation);
  }

  pi.registerCommand("dictate", {
    description: "Toggle ChatGPT dictation into the editor; /dictate cancel discards audio",
    handler: (args, ctx) => {
      if (args.trim() === "cancel") {
        cancel();
        ctx.ui.notify("Dictation cancelled; nothing was inserted or submitted.", "info");
      } else if (args.trim()) {
        ctx.ui.notify("Usage: /dictate (start/stop) or /dictate cancel", "info");
      } else toggle(ctx);
    },
  });
  pi.registerShortcut("ctrl+alt+d", {
    description: "Start/stop dictation into the editor (never auto-send)",
    handler: toggle,
  });
  pi.on("session_before_switch", cancel);
  pi.on("session_before_branch", cancel);
  pi.on("session_before_tree", cancel);
  pi.on("session_shutdown", cancel);
}
