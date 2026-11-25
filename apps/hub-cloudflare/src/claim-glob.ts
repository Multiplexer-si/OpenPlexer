// The claim dialect counts UTF-16 units, including for the limits in internal/policy/glob.go.
const maxPath = 4096;
const maxCost = 3000;
const maxWork = 60_000_000;
const unbounded = -1;

type Unit = { kind: "unit"; holds: (unit: string) => boolean; work: number };
type Edge = { kind: "start" } | { kind: "end" };
type Node = Unit | Edge
  | { kind: "sequence" | "alternatives"; nodes: Node[] }
  | { kind: "repeat"; node: Node; low: number; high: number; least: number }
  | { kind: "look"; node: Node; behind: boolean; negated: boolean };
type Instruction = Unit | Edge
  | { kind: "match" }
  | { kind: "run"; unit: Unit }
  | { kind: "fork" | "jump"; offset: number }
  | { kind: "test"; index: number; negated: boolean };
type Assertion = { code: Instruction[]; backward: boolean };

const slashless: Unit = { kind: "unit", holds: (unit) => unit !== "/", work: 1 };
const dot: Unit = { kind: "unit", holds: (unit) => !/[\n\r\u2028\u2029]/.test(unit), work: 1 };
const never: Unit = { kind: "unit", holds: () => false, work: 1 };
const start: Edge = { kind: "start" };
const end: Edge = { kind: "end" };
const match: Instruction = { kind: "match" };

function literal(text: string): Unit {
  return { kind: "unit", holds: (unit) => unit === text, work: 1 };
}

function characterClass(text: string): Unit {
  const negated = text[0] === "^";
  if (negated) text = text.slice(1);
  const spans: [string, string][] = [];
  for (let i = 0; i < text.length; i++) {
    const low = text[i];
    const high = i + 2 < text.length && text[i + 1] === "-" ? text[i += 2] : low;
    if (low > high) throw new SyntaxError("invalid claim glob range");
    spans.push([low, high]);
  }
  return { kind: "unit", holds: (unit) => spans.some(([low, high]) => unit >= low && unit <= high) !== negated,
    work: spans.length ? 2 + spans.length : 1 };
}

type Part = Unit | { kind: "wildcard"; unit: Unit; directory: boolean } | string;

function globParts(glob: string, exotic: boolean): Part[] {
  const parts: Part[] = [];
  let noCloser = false;
  for (let at = 0; at < glob.length;) {
    const from = at;
    const unit = glob[at++];
    if (unit === "*") {
      while (glob[at] === "*") at++;
      const across = at - from > 1;
      const directory = across && (from === 0 || glob[from - 1] === "/") && glob[at] === "/";
      if (directory) at++;
      parts.push({ kind: "wildcard", unit: across ? dot : slashless, directory });
    } else if (unit === "?") {
      parts.push(slashless);
    } else if (unit === "[" && !noCloser) {
      // The first ] can be a class member, so start the search after it.
      const close = glob.indexOf("]", at + 1);
      if (close < 0) {
        noCloser = true;
        parts.push(literal(unit));
      } else {
        const members = glob.slice(at, close).replace(/^!/, "^");
        if (exotic) parts.push(...("[" + members + "]").split(""));
        else parts.push(characterClass(members));
        at = close + 1;
      }
    } else {
      parts.push(literal(unit));
    }
  }
  return parts;
}

function repeat(node: Node, low: number, high: number): Node {
  return { kind: "repeat", node, low, high, least: minUnits(node) };
}

function minUnits(node: Node): number {
  switch (node.kind) {
    case "unit": return 1;
    case "repeat": return Math.min(0x7fffffff, node.low * node.least);
    case "sequence": return Math.min(0x7fffffff, node.nodes.reduce((sum, term) => sum + minUnits(term), 0));
    case "alternatives": return Math.min(...node.nodes.map(minUnits));
    default: return 0;
  }
}

// A class beginning with ] can expose regexp syntax. Parse that syntax into
// the same bounded automaton as ordinary globs.
function parseExotic(parts: Part[]): Node {
  let at = 0;
  let groups = 0;
  const names = new Set<string>();

  function take(character: string): boolean {
    if (parts[at] !== character) return false;
    at++;
    return true;
  }

  function textWhile(pattern: RegExp): string {
    let text = "";
    for (let part = parts[at]; typeof part === "string" && pattern.test(part); part = parts[++at]) text += part;
    return text;
  }

  function bounds(): [number, number] | undefined {
    if (take("*")) return [0, unbounded];
    if (take("+")) return [1, unbounded];
    if (take("?")) return [0, 1];
    const from = at;
    if (take("{")) {
      const digits = textWhile(/[0-9]/);
      if (digits) {
        const low = Math.min(Number(digits), 0x7fffffff);
        const upper = take(",") ? textWhile(/[0-9]/) : digits;
        const high = upper ? Math.min(Number(upper), 0x7fffffff) : unbounded;
        if (take("}")) return [low, high];
      }
    }
    at = from;
  }

  function quantified(node: Node): Node {
    const count = bounds();
    if (!count) return node;
    const [low, high] = count;
    if (high !== unbounded && low > high) throw new SyntaxError("invalid claim glob count");
    take("?");
    return repeat(node, low, high);
  }

  function group(): Node {
    if (++groups > 1000) throw new SyntaxError("too many claim glob groups");
    let assertion: { behind: boolean; negated: boolean } | undefined;
    if (take("?")) {
      const behind = take("<");
      if (take("=")) assertion = { behind, negated: false };
      else if (take("!")) assertion = { behind, negated: true };
      else if (behind) {
        const name = textWhile(/[A-Za-z0-9_$]/);
        if (!/^[A-Za-z_$]/.test(name) || !take(">") || names.has(name)) {
          throw new SyntaxError("invalid claim glob group name");
        }
        names.add(name);
      } else if (!take(":")) throw new SyntaxError("invalid claim glob group");
    }
    const node = alternatives();
    if (!take(")")) throw new SyntaxError("unclosed claim glob group");
    if (!assertion) return quantified(node);
    const look: Node = { kind: "look", node, ...assertion };
    return look.behind ? look : quantified(look);
  }

  function term(): Node {
    const part = parts[at];
    if (typeof part !== "string") {
      at++;
      if (part.kind === "unit") return quantified(part);
      const run = repeat(part.unit, 0, unbounded);
      return quantified(part.directory
        ? repeat({ kind: "sequence", nodes: [run, literal("/")] }, 0, 1) : run);
    }
    if (bounds()) throw new SyntaxError("claim glob quantifier with no term");
    at++;
    if (part === "^") return start;
    if (part === "$") return end;
    if (part === ".") return quantified(dot);
    if (part === "(") return group();
    if (part === "[") {
      const members = textWhile(/[^\]]/);
      take("]");
      return quantified(characterClass(members));
    }
    return quantified(literal(part));
  }

  function alternatives(): Node {
    const branches: Node[] = [];
    do {
      const nodes: Node[] = [];
      while (at < parts.length && parts[at] !== "|" && parts[at] !== ")") nodes.push(term());
      branches.push({ kind: "sequence", nodes });
    } while (take("|"));
    return { kind: "alternatives", nodes: branches };
  }

  const root = alternatives();
  if (at < parts.length) throw new SyntaxError("unopened claim glob group");
  return root;
}

class MatchBudget {
  private remaining = maxWork;

  spend(work: number): void {
    this.remaining -= work;
    if (this.remaining < 0) throw new RangeError("claim glob is over its work budget");
  }
}

class Compiler {
  readonly assertions: Assertion[] = [];

  constructor(private limit: number, private readonly length: number, private readonly budget: MatchBudget) {}

  private add(code: Instruction[], ...instructions: Instruction[]): void {
    this.limit -= instructions.length;
    if (this.limit < 0) throw new RangeError("claim glob exceeds its size limit");
    this.budget.spend(instructions.length);
    code.push(...instructions);
  }

  emit(code: Instruction[], node: Node, backward = false): void {
    switch (node.kind) {
      case "sequence":
        for (const term of backward ? node.nodes.toReversed() : node.nodes) this.emit(code, term, backward);
        break;
      case "alternatives": {
        const jumps: { index: number; instruction: { kind: "jump"; offset: number } }[] = [];
        for (const [i, branch] of node.nodes.entries()) {
          const index = code.length;
          const state: { kind: "fork"; offset: number } = { kind: "fork", offset: 0 };
          if (i < node.nodes.length - 1) this.add(code, state);
          this.emit(code, branch, backward);
          if (i < node.nodes.length - 1) {
            const instruction: { kind: "jump"; offset: number } = { kind: "jump", offset: 0 };
            jumps.push({ index: code.length, instruction });
            this.add(code, instruction);
            state.offset = code.length - index;
          }
        }
        for (const { index, instruction } of jumps) instruction.offset = code.length - index;
        break;
      }
      case "repeat":
        this.emitRepeat(code, node, backward);
        break;
      case "look": {
        const assertion: Assertion = { code: [], backward: !node.behind };
        this.emit(assertion.code, node.node, assertion.backward);
        assertion.code.push(match);
        const index = this.assertions.push(assertion) - 1;
        this.add(code, { kind: "test", index, negated: node.negated });
        break;
      }
      default:
        this.add(code, node);
    }
  }

  private emitRepeat(code: Instruction[], node: Extract<Node, { kind: "repeat" }>, backward: boolean): void {
    const { low, least } = node;
    let { high } = node;
    if (high === 0) return;
    const body: Instruction[] = [];
    this.emit(body, node.node, backward);
    this.budget.spend(2 * body.length);
    if (!body.some(({ kind }) => kind === "unit" || kind === "run")) {
      if (low !== 0) code.push(...body);
      return;
    }
    this.limit += body.length;
    const most = least ? Math.floor(this.length / least) : this.length + 1;
    if (least && low > most) {
      this.add(code, never);
      return;
    }
    if (high > most) high = unbounded;
    for (let i = 0; i < low; i++) this.add(code, ...body);
    if (high === unbounded) {
      if (body.length === 1 && body[0].kind === "unit") this.add(code, { kind: "run", unit: body[0] });
      else this.add(code, { kind: "fork", offset: body.length + 2 }, ...body,
        { kind: "jump", offset: -body.length - 1 });
    } else {
      for (let i = high - low; i > 0; i--) this.add(code, { kind: "fork", offset: i * (body.length + 1) }, ...body);
    }
  }
}

function runWork(code: Instruction[], length: number): number {
  let steps = 3;
  for (const instruction of code) {
    switch (instruction.kind) {
      case "fork": steps += 3; break;
      case "unit": steps += 2 + instruction.work; break;
      case "run": steps += 3 + instruction.unit.work; break;
      default: steps += 2;
    }
  }
  return (length + 1) * steps + 2 * code.length;
}

function run(code: Instruction[], path: string, tests: Uint8Array[], backward = false, found?: Uint8Array): boolean {
  const length = path.length;
  const seen = new Int32Array(code.length);
  let states: number[] = [];
  let next: number[] = [];
  const pending: number[] = [];
  const anchored = !found && code[0].kind === "start";

  function add(output: number[], pc: number, at: number, stamp: number): void {
    pending.push(pc);
    while (pending.length) {
      const index = pending.pop() ?? 0;
      if (seen[index] === stamp) continue;
      seen[index] = stamp;
      const instruction = code[index];
      switch (instruction.kind) {
        case "fork": pending.push(index + instruction.offset, index + 1); break;
        case "jump": pending.push(index + instruction.offset); break;
        case "start": if (at === 0) pending.push(index + 1); break;
        case "end": if (at === length) pending.push(index + 1); break;
        case "test":
          if (Boolean(tests[instruction.index][at]) !== instruction.negated) pending.push(index + 1);
          break;
        case "run": pending.push(index + 1); output.push(index); break;
        default: output.push(index);
      }
    }
  }

  for (let step = 0; step <= length; step++) {
    const at = backward ? length - step : step;
    const to = backward ? at - 1 : at + 1;
    if (!anchored || step === 0) add(states, 0, at, step + 1);
    if (anchored && states.length === 0) return false;
    const unit = path[Math.min(at, to)];
    for (const pc of states) {
      const instruction = code[pc];
      if (instruction.kind === "match") {
        if (!found) return true;
        found[at] = 1;
      } else if (step < length) {
        if (instruction.kind === "unit" && instruction.holds(unit)) add(next, pc + 1, to, step + 2);
        else if (instruction.kind === "run" && instruction.unit.holds(unit)) add(next, pc, to, step + 2);
      }
    }
    states.length = 0;
    [states, next] = [next, states];
  }
  return false;
}

function globMatches(glob: string, path: string): boolean {
  if (glob.length > maxCost) throw new RangeError("claim glob exceeds its size limit");
  const budget = new MatchBudget();
  budget.spend(new TextEncoder().encode(glob).length);
  const exotic = glob.includes("[]");
  const parts = globParts(glob, exotic);
  let code: Instruction[] = [];
  let assertions: Assertion[] = [];
  if (exotic) {
    const root = parseExotic(["^", ...parts, "$"]);
    let compiler = new Compiler(maxCost - glob.length, maxPath, budget);
    compiler.emit(code, root);
    if (path.length !== maxPath) {
      compiler = new Compiler(maxCost, path.length, budget);
      code = [];
      compiler.emit(code, root);
    }
    assertions = compiler.assertions;
  } else {
    code.push(start);
    for (const part of parts) {
      if (typeof part === "string") throw new Error("unexpected claim glob syntax");
      if (part.kind === "unit") code.push(part);
      else if (part.directory) code.push({ kind: "fork", offset: 3 }, { kind: "run", unit: part.unit }, literal("/"));
      else code.push({ kind: "run", unit: part.unit });
    }
    code.push(end);
    if (glob.length + code.length > maxCost) throw new RangeError("claim glob exceeds its size limit");
    budget.spend(code.length + 1);
  }
  code.push(match);
  budget.spend(runWork(code, path.length));
  for (const assertion of assertions) budget.spend(path.length + 1 + runWork(assertion.code, path.length));
  const tests: Uint8Array[] = [];
  for (const assertion of assertions) {
    const found = new Uint8Array(path.length + 1);
    run(assertion.code, path, tests, assertion.backward, found);
    tests.push(found);
  }
  return run(code, path, tests);
}

function normalize(entry: string): string {
  return entry.replaceAll("\\", "/").replace(/^(?:\.\/)+/, "").replace(/\/+/g, "/");
}

export function patternsOverlap(a: string, b: string): boolean {
  if (a.length > maxPath || b.length > maxPath) return true;
  a = normalize(a);
  b = normalize(b);
  const wildcardA = a.search(/[*?\[]/);
  const wildcardB = b.search(/[*?\[]/);
  if (wildcardA < 0 && wildcardB < 0) return a === b;
  try {
    if (wildcardA < 0) return globMatches(b, a);
    if (wildcardB < 0) return globMatches(a, b);
    const prefixA = a.slice(0, wildcardA);
    const prefixB = b.slice(0, wildcardB);
    return globMatches(a, prefixB) || globMatches(b, prefixA) || prefixA.startsWith(prefixB) || prefixB.startsWith(prefixA);
  } catch (error) {
    if (error instanceof SyntaxError || error instanceof RangeError) return true;
    throw error;
  }
}

export function pathsOverlap(a: string[], b: string[]): boolean {
  return a.some((left) => b.some((right) => patternsOverlap(left, right)));
}
