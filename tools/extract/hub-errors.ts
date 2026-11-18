import { readFileSync } from "node:fs";
import { join } from "node:path";

const SERVER = "apps/hub/src/ws/server.ts";
const MODERATION = "apps/hub/src/room/moderation.ts";

export const HUB_ERROR_SOURCES = [SERVER, "apps/hub/src/ws/client-version.ts", MODERATION];

/** `fatal` sends an error and closes the connection. The other three send an error and keep the connection. */
const SENDERS = ["fatal", "error", "postError", "claimErr"];

/** The index after the string or template literal that starts at `from`. */
function afterLiteral(source: string, from: number): number {
  const quote = source[from]!;
  for (let i = from + 1; i < source.length; i++) {
    if (source[i] === "\\") i++;
    else if (source[i] === quote) return i + 1;
    else if (quote === "`" && source.startsWith("${", i)) i = afterArguments(source, i + 2, "}").end - 1;
  }
  throw new Error(`${SERVER}: a literal that starts at ${from} does not end`);
}

/** The top-level arguments from `from` up to the `close` that ends them, and the index after that `close`. */
function afterArguments(source: string, from: number, close: string): { args: string[]; end: number } {
  const args: string[] = [];
  let start = from;
  for (let i = from; i < source.length; ) {
    const c = source[i]!;
    if (c === close) return { args: [...args, source.slice(start, i).trim()].filter((arg) => arg !== ""), end: i + 1 };
    if (c === '"' || c === "'" || c === "`") i = afterLiteral(source, i);
    else if (c === "(") i = afterArguments(source, i + 1, ")").end;
    else if (c === "[") i = afterArguments(source, i + 1, "]").end;
    else if (c === "{") i = afterArguments(source, i + 1, "}").end;
    else {
      if (c === ",") {
        args.push(source.slice(start, i).trim());
        start = i + 1;
      }
      i++;
    }
  }
  throw new Error(`${SERVER}: the arguments that start at ${from} do not end`);
}

/**
 * Each error code that the socket server of the hub names in its source, with the close code that follows it, or
 * null if the hub keeps the connection. The server needs a database, so the extractor reads its source and does not
 * run it. A code that the server takes from another service at run time is not in the result.
 */
export function hubErrorCodes(root: string, constants: Record<string, number>): Map<string, number | null> {
  const source = readFileSync(join(root, SERVER), "utf8");
  const moderation = /type ModerationCode = ([^;]+);/.exec(readFileSync(join(root, MODERATION), "utf8"));
  if (!moderation) throw new Error(`${MODERATION}: found no ModerationCode`);
  const literals = (text: string): string[] => [...text.matchAll(/"([a-z_]+)"/g)].map((match) => match[1]!);

  const found = new Map<string, number | null>();
  const calls = new Map(SENDERS.map((name) => [name, 0]));
  for (const call of source.matchAll(new RegExp(`(?<![.\\w])(${SENDERS.join("|")})\\(`, "g"))) {
    const name = call[1]!;
    const { args } = afterArguments(source, call.index + call[0].length, ")");
    calls.set(name, calls.get(name)! + 1);
    let closeCode: number | null = null;
    if (name === "fatal") {
      const last = args.at(-1)!;
      closeCode = /^\d+$/.test(last) ? Number(last) : (constants[last] ?? null);
      if (closeCode === null) throw new Error(`${SERVER}: fatal closes with ${last}, which is not a number that the extractor knows`);
    }
    const codes = args[0] === "m.code" ? literals(moderation[1]!) : literals(args[0]!);
    for (const code of codes) {
      if (found.has(code) && found.get(code) !== closeCode) throw new Error(`${SERVER}: ${code} closes with ${found.get(code)} and with ${closeCode}`);
      found.set(code, closeCode);
    }
  }
  for (const [name, count] of calls) {
    if (count === 0) throw new Error(`${SERVER}: found no call of ${name}`);
  }
  return new Map([...found].sort(([a], [b]) => (a < b ? -1 : 1)));
}
