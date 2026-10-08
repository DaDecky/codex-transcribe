import assert from "node:assert/strict";
import { createServer } from "node:http";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { transcribe } from "./audio.js";

async function fixture(t, handler) {
  const directory = await mkdtemp(join(tmpdir(), "dictation-test-"));
  const file = join(directory, "audio.wav");
  await writeFile(file, Buffer.alloc(46));
  const server = createServer(handler);
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  t.after(async () => {
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
    await rm(directory, { recursive: true, force: true });
  });
  return { file, endpoint: `http://127.0.0.1:${server.address().port}` };
}

for (const [body, expected] of [
  ["not JSON", /did not return a text field/],
  [JSON.stringify({ text: 42 }), /did not return a text field/],
  [JSON.stringify({ text: " \n\t " }), /No speech was recognized/],
]) {
  test(`reject unusable transcript: ${body}`, async t => {
    const { file, endpoint } = await fixture(t, (_request, response) => response.end(body));
    await assert.rejects(transcribe(file, endpoint, "", new AbortController().signal), expected);
  });
}

test("upstream failure identifies status/code without exposing private error bodies", async t => {
  const { file, endpoint } = await fixture(t, (_request, response) => {
    response.writeHead(502);
    response.end(JSON.stringify({ error: { code: "codex_auth_rejected", message: "private bearer token", upstream_body: "private recording" } }));
  });
  await assert.rejects(transcribe(file, endpoint, "", new AbortController().signal), error => {
    assert.match(error.message, /HTTP 502 \(codex_auth_rejected\)/);
    assert.doesNotMatch(error.message, /private bearer token|private recording/);
    return true;
  });
});

test("cancellation while a response is pending discards the eventual transcript", async t => {
  let received;
  const requestReceived = new Promise(resolve => { received = resolve; });
  let pendingResponse;
  const { file, endpoint } = await fixture(t, (_request, response) => {
    pendingResponse = response;
    received();
  });
  const controller = new AbortController();
  const result = transcribe(file, endpoint, "", controller.signal);
  await requestReceived;
  controller.abort();
  pendingResponse.end(JSON.stringify({ text: "Must not be inserted after cancellation" }));
  await assert.rejects(result, { name: "AbortError" });
});

test("an empty WAV is rejected before contacting the server", async t => {
  let requests = 0;
  const { file, endpoint } = await fixture(t, (_request, response) => { requests++; response.end('{"text":"unexpected"}'); });
  await writeFile(file, Buffer.alloc(44));
  await assert.rejects(transcribe(file, endpoint, "", new AbortController().signal), /No audio was recorded/);
  assert.equal(requests, 0);
});
