import { expect, test } from "vitest";
import { roomStream, streamLimits } from "../src/stream";
import type { RoomPage } from "../src/stream";

test("counts the initial read toward the 400-call budget and closes after the last page", async () => {
  const page = (cursor: number) => ({
    ok: true, cursor, frames: [{ seq: cursor + 1, line: JSON.stringify({ seq: cursor + 1 }) }],
  } satisfies RoomPage);
  const cursors = [0];
  const signal = new AbortController().signal;
  const stream = roomStream(signal, "demo", page(0), async (cursor) => {
    cursors.push(cursor);
    return page(cursor);
  }, streamLimits, Date.now());
  const lines = (await new Response(stream).text()).trimEnd().split("\n");
  expect(cursors).toEqual(Array.from({ length: 400 }, (_, i) => i));
  expect(lines).toEqual(cursors.map((cursor) => JSON.stringify({ seq: cursor + 1 })));
});

test("emits a heartbeat and stops waiting when the request signal aborts", async () => {
  const request = new AbortController();
  const page = { ok: true, cursor: 0, frames: [] } satisfies RoomPage;
  let reads = 0;
  const stream = roomStream(request.signal, "demo", page, async () => {
    reads++;
    return page;
  }, { ...streamLimits, pollMs: 60_000 }, Date.now());
  const reader = stream.getReader();
  try {
    expect((await reader.read()).value).toEqual(new Uint8Array([10]));
    const pending = reader.read();
    request.abort();
    expect((await pending).done).toBe(true);
    expect(reads).toBe(0);
  } finally {
    await reader.cancel();
  }
});
