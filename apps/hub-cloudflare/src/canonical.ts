export type JsonValue = null | boolean | number | string | JsonValue[] | JsonObject;
export type JsonObject = { [name: string]: JsonValue };

export function isJsonObject(value: JsonValue | undefined): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

type ParseOptions = { rejectNumberOverflow?: boolean } & (
  | { duplicates?: WeakSet<JsonObject>; loneSurrogates?: "reject" }
  | { duplicates?: never; loneSurrogates: "replace" }
);

type Container =
  | { kind: "array"; value?: JsonValue[]; index: number }
  | { kind: "object"; value?: JsonObject; key?: string; names: Set<string> };

export function parseJSON(input: string | Uint8Array, options: ParseOptions = {}): JsonValue {
  const source = typeof input === "string"
    ? input
    : new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(input);
  // Native parsing checks syntax and preserves the spec's double and UTF-16 semantics.
  const root: JsonValue = JSON.parse(source);
  const stack: Container[] = [];
  const replacements: string[] = [];
  let copied = 0;
  // Scan the source because a reviver cannot see members that were overwritten.
  const tokens = options.rejectNumberOverflow
    ? /"(?:[^"\\]|\\[^])*"|[{}\[\],]|-?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/g
    : /"(?:[^"\\]|\\[^])*"|[{}\[\],]/g;
  for (const match of source.matchAll(tokens)) {
    const token = match[0];
    const parent = stack.at(-1);
    if (token === "{" || token === "[") {
      if (stack.length === 10_000) throw new SyntaxError("JSON exceeds 10000 levels");
      const value = !parent ? root : parent.kind === "array"
        ? parent.value?.[parent.index]
        : parent.key === undefined ? undefined : parent.value?.[parent.key];
      if (token === "[") {
        stack.push({ kind: "array", value: Array.isArray(value) ? value : undefined, index: 0 });
      } else {
        stack.push({ kind: "object", value: isJsonObject(value) ? value : undefined, names: new Set() });
      }
    } else if (token === "}" || token === "]") {
      stack.pop();
    } else if (token === ",") {
      if (parent?.kind === "array") parent.index++;
      if (parent?.kind === "object") parent.key = undefined;
    } else if (token[0] !== '"') {
      if (!Number.isFinite(Number(token))) throw new SyntaxError("JSON number exceeds the finite double range");
    } else {
      if (options.loneSurrogates) {
        const decoded: string = JSON.parse(token);
        if (!decoded.isWellFormed()) {
          if (options.loneSurrogates === "reject") throw new SyntaxError("JSON contains a lone surrogate");
          replacements.push(source.slice(copied, match.index), JSON.stringify(decoded.toWellFormed()));
          copied = match.index + token.length;
        }
      }
      if (options.duplicates && parent?.kind === "object" && parent.key === undefined) {
        const key: string = JSON.parse(token);
        if (parent.value && parent.names.has(key)) options.duplicates.add(parent.value);
        parent.key = key;
        parent.names.add(key);
      }
    }
  }
  if (copied > 0) {
    replacements.push(source.slice(copied));
    return JSON.parse(replacements.join(""));
  }
  return root;
}

export function hasDuplicateMembers(value: JsonValue, duplicates: WeakSet<JsonObject>): boolean {
  const pending = [value];
  while (pending.length > 0) {
    const item = pending.pop();
    if (Array.isArray(item)) {
      for (const element of item) pending.push(element);
    } else if (isJsonObject(item)) {
      if (duplicates.has(item)) return true;
      for (const element of Object.values(item)) pending.push(element);
    }
  }
  return false;
}

export function canonicalJSON(value: JsonValue): string {
  type Part = { value: JsonValue } | { text: string };
  const pending: Part[] = [{ value }];
  const output: string[] = [];
  for (let part = pending.pop(); part; part = pending.pop()) {
    if ("text" in part) {
      output.push(part.text);
      continue;
    }
    const item = part.value;
    if (Array.isArray(item)) {
      output.push("[");
      pending.push({ text: "]" });
      for (let i = item.length - 1; i >= 0; i--) {
        pending.push({ value: item[i] ?? null });
        if (i > 0) pending.push({ text: "," });
      }
    } else if (isJsonObject(item)) {
      output.push("{");
      pending.push({ text: "}" });
      const names = Object.keys(item).sort();
      for (let i = names.length - 1; i >= 0; i--) {
        const name = names[i];
        pending.push({ value: item[name] }, { text: JSON.stringify(name) + ":" });
        if (i > 0) pending.push({ text: "," });
      }
    } else {
      output.push(JSON.stringify(item));
    }
  }
  return output.join("");
}

export function canonicalize(input: string | Uint8Array): string {
  return canonicalJSON(parseJSON(input));
}
