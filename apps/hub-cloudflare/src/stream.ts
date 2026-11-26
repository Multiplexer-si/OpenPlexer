import { unauthorized } from "./http";
import type { eventPage } from "./room";

export const streamHeaders = { "Content-Type": "application/x-ndjson", "Cache-Control": "no-store" };
export const streamLimits = { pollMs: 1_000, durationMs: 300_000, calls: 400, writeMs: 5_000 };

export type RoomRead = { room: string; hash: ArrayBuffer; after: string | number };
type Page = { ok: true; cursor: number; frames: ReturnType<typeof eventPage> };
export type RoomPage = Page | { ok: false; status: number; message: string };

const utf8 = new TextEncoder();

export function roomStream(
  signal: AbortSignal, room: string, initial: Page, read: (after: number) => Promise<RoomPage>,
  limits: typeof streamLimits, remainingMs: () => number,
): ReadableStream<Uint8Array> {
  let cursor = initial.cursor;
  let page: Page["frames"] | undefined = initial.frames;
  let index = 0;
  let calls = 1;
  let caughtUp = false;
  let stopped = false;
  let close: () => void;
  let wakePoll: (() => void) | undefined;
  let budgetTimer: ReturnType<typeof setTimeout>;
  let writeTimer: ReturnType<typeof setTimeout>;

  function cleanup() {
    stopped = true;
    page = undefined;
    clearTimeout(budgetTimer);
    clearTimeout(writeTimer);
    signal.removeEventListener("abort", finish);
    wakePoll?.();
  }

  function finish() {
    if (stopped) return;
    cleanup();
    close();
  }

  function isFinished() {
    if (remainingMs() <= 0) finish();
    return stopped;
  }

  return new ReadableStream({
    start(controller) {
      close = () => controller.close();
      if (signal.aborted) {
        finish();
        return;
      }
      signal.addEventListener("abort", finish, { once: true });
      budgetTimer = setTimeout(finish, remainingMs());
      writeTimer = setTimeout(finish, limits.writeMs);
    },
    async pull(controller) {
      clearTimeout(writeTimer);
      if (caughtUp) {
        await new Promise<void>((resolve) => {
          const poll = setTimeout(resolve, limits.pollMs);
          wakePoll = () => { clearTimeout(poll); resolve(); };
        });
        wakePoll = undefined;
      }
      if (isFinished()) return;
      try {
        if (!page) {
          calls++;
          const next = await read(cursor);
          if (isFinished()) return;
          if (!next.ok) {
            if (next.status === 401) {
              controller.enqueue(utf8.encode(JSON.stringify({
                v: 1, type: "error", room, ts: new Date().toISOString(), sender: { id: "hub", kind: "server" },
                payload: { code: "unauthorized", message: unauthorized().message },
              }) + "\n"));
            }
            finish();
            return;
          }
          page = next.frames;
          index = 0;
        }
        const frame = page[index++];
        if (frame) {
          controller.enqueue(utf8.encode(frame.line + "\n"));
          cursor = frame.seq;
        } else {
          controller.enqueue(utf8.encode("\n"));
        }
        caughtUp = !frame;
        if (index >= page.length) {
          page = undefined;
          if (calls === limits.calls) {
            finish();
            return;
          }
        }
        // A pull releases one frame. Close if the runtime stops draining the response.
        writeTimer = setTimeout(finish, limits.writeMs);
      } catch {
        finish();
      }
    },
    cancel() { cleanup(); },
  }, { highWaterMark: 0 });
}
