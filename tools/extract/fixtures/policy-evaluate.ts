import { asCommitted, type Json } from "../output.ts";
import type { Reference } from "../reference.ts";
import { fixtureFile, fixturePath, type Case, type FixtureFile } from "./format.ts";

type Query = { [key: string]: Json };
type Bundle = { [key: string]: Json };
type Authored = [name: string, why: string, query: Query];

const NOON = "2026-01-15T12:00:00.000Z";
const SOURCES = ["packages/protocol/src/policy/evaluate.ts", "packages/protocol/src/policy/glob.ts"];

/** A query in repository `api` at noon. `extra` overrides any field. */
const ask = (member: string, action: string, path: string, extra: Query = {}): Query => ({ repo: "api", path, action, member, now: NOON, ...extra });
const run = (member: string, command: string, extra: Query = {}): Query => ask(member, "exec", "", { command, ...extra });
const role = (grants: { read?: string[]; write?: string[]; exec?: string[]; push?: string[] }): Json => ({ read: [], write: [], exec: [], ...grants });
const bundle = (parts: Bundle): Bundle => ({ version: 1, project_id: "prj-fixture", repos: ["api", "web"], ...parts });

function file(reference: Reference, name: string, description: string, policy: Bundle, authored: Authored[]): [string, FixtureFile] {
  const shared = asCommitted({ bundle: policy });
  const cases = authored.map(([caseName, why, query]): Case => {
    const input = asCommitted(query);
    const { decision, area, rule } = reference.protocol.evaluate(shared.bundle, { ...input, now: new Date(input.now as string) });
    return { name: caseName, why, input, expected: area === undefined ? { decision } : { decision, area }, informational: { rule } };
  });
  return [fixturePath("policy", name), fixtureFile({ contract: "policy", operation: "evaluate", description, sources: SOURCES, shared, cases })];
}

function defaultsAndRoles(reference: Reference): [string, FixtureFile] {
  const policy = bundle({
    areas: [
      { name: "backend", patterns: ["api:src/**"] },
      { name: "frontend", patterns: ["web:src/**"] },
      { name: "docs", patterns: ["*:docs/**"] },
      { name: "ci", patterns: [".github/**"] },
      { name: "colon", patterns: ["api:src:x/**"] },
    ],
    roles: { backend: role({ write: ["backend", "colon"] }), frontend: role({ write: ["frontend", "docs"] }), reader: role({ read: ["docs"] }) },
    members: { "mem-ana": "backend", "mem-ben": "frontend", "mem-dan": "ghost", "mem-rae": "reader" },
  });
  return file(reference, "evaluate-defaults-and-roles", "Default results outside any area, role grants, and how a pattern selects a repository.", policy, [
    ["read outside every area is allowed", "A read of a path that no area matches is allowed by default.", ask("mem-ana", "read", "README.md")],
    ["write outside every area is denied", "A write of a path that no area matches is denied by default.", ask("mem-ana", "write", "README.md")],
    ["write in an area granted to the role is allowed", "A role that lists the area under write may write in it.", ask("mem-ana", "write", "src/orders/cart.ts")],
    ["write in an area not granted to the role is denied", "A role may write only in the areas that it lists under write.", ask("mem-ana", "write", "src/pages/cart.tsx", { repo: "web" })],
    ["read in an area that is not private is allowed without a grant", "Only a private area needs a read grant.", ask("mem-ana", "read", "src/pages/cart.tsx", { repo: "web" })],
    ["write by a member with no role is denied", "A member that the bundle does not list has no grants.", ask("mem-eve", "write", "src/orders/cart.ts")],
    ["read by a member with no role is allowed in an area that is not private", "The default read does not depend on a role.", ask("mem-eve", "read", "src/orders/cart.ts")],
    ["write by a member whose role is not defined is denied", "A member mapped to a role name that the bundle does not define has no grants.", ask("mem-dan", "write", "src/orders/cart.ts")],
    ["a pattern with a repository does not match another repository", "`api:src/**` matches only in the repository `api`.", ask("mem-ana", "write", "src/orders/cart.ts", { repo: "tools" })],
    ["a read grant does not allow a write", "A role that lists an area under read only may not write in it.", ask("mem-rae", "write", "docs/setup.md")],
    ["a pattern with * matches every repository", "`*:docs/**` matches in any repository.", ask("mem-ben", "write", "docs/setup.md", { repo: "api" })],
    ["a pattern without a repository matches every repository", "A pattern with no `:` is the same as `*:` followed by the pattern.", ask("mem-ana", "read", ".github/workflows/ci.yml", { repo: "web" })],
    ["the repository name is compared without case", "`API` selects the patterns of `api`.", ask("mem-ana", "write", "src/orders/cart.ts", { repo: "API" })],
    ["a pattern is split at its first colon", "`api:src:x/**` is the repository `api` and the glob `src:x/**`, so it matches `src:x/y` in `api`.", ask("mem-ana", "write", "src:x/y")],
  ]);
}

function specificity(reference: Reference): [string, FixtureFile] {
  const policy = bundle({
    areas: [
      { name: "src", patterns: ["api:src/**"] },
      { name: "billing", patterns: ["api:src/billing/**"] },
      { name: "tie-a", patterns: ["api:shared/**"] },
      { name: "tie-b", patterns: ["api:shared/**"] },
      { name: "pkg-any", patterns: ["api:pkg/**"] },
      { name: "pkg-nested", patterns: ["api:pkg/*/**"] },
      { name: "lib", patterns: ["api:lib/**", "api:lib/vendor/special/**"] },
      { name: "vendor", patterns: ["api:lib/vendor/**"] },
      { name: "long-prefix", patterns: ["api:abc/d**"] },
      { name: "more-segments", patterns: ["api:abc/*/e"] },
    ],
    roles: {
      dev: role({ write: ["src", "tie-a", "pkg-nested", "lib", "long-prefix"] }),
      biller: role({ write: ["billing", "tie-b", "pkg-any"] }),
      admin: role({ write: ["tie-a", "tie-b"] }),
    },
    members: { "mem-ana": "dev", "mem-ben": "biller", "mem-lee": "admin" },
  });
  return file(reference, "evaluate-specificity", "Which area decides when several areas match one path.", policy, [
    ["the only matching area decides", "With one matching area, that area decides.", ask("mem-ana", "write", "src/app.ts")],
    ["the more specific area denies a member granted only the broader area", "The area whose pattern has the longer literal prefix wins.", ask("mem-ana", "write", "src/billing/invoice.ts")],
    ["the more specific area allows a member granted only that area", "The area whose pattern has the longer literal prefix wins.", ask("mem-ben", "write", "src/billing/invoice.ts")],
    ["a grant on the more specific area does not open the broader area", "A grant applies only where its area wins.", ask("mem-ben", "write", "src/app.ts")],
    ["an exact tie denies when the first area allows and the second denies", "Between two areas of equal specificity, the one that denies wins.", ask("mem-ana", "write", "shared/util.ts")],
    ["an exact tie denies when the first area denies and the second allows", "The tie rule does not depend on the order of the areas.", ask("mem-ben", "write", "shared/util.ts")],
    ["an exact tie allows when both areas allow, and reports the first area", "When neither area denies, the area listed first in the bundle is reported.", ask("mem-lee", "write", "shared/util.ts")],
    ["equal prefixes: the pattern with more segments wins and allows", "When the literal prefixes have the same length, the pattern with more `/`-separated segments wins.", ask("mem-ana", "write", "pkg/a/b.ts")],
    ["equal prefixes: the pattern with more segments wins and denies", "When the literal prefixes have the same length, the pattern with more `/`-separated segments wins.", ask("mem-ben", "write", "pkg/a/b.ts")],
    ["an area competes with its most specific matching pattern", "Each area is ranked by the best of its patterns that match the path.", ask("mem-ana", "write", "lib/vendor/special/x.ts")],
    ["a pattern that does not match the path does not rank its area", "Only the patterns that match the path count.", ask("mem-ana", "write", "lib/vendor/y.ts")],
    ["a longer literal prefix wins over more segments", "The prefix length is compared first. `abc/d**` has the prefix `abc/d` and two segments, and it wins over `abc/*/e`, which has a shorter prefix and three segments.", ask("mem-ana", "write", "abc/d/e")],
  ]);
}

function privateAndSystem(reference: Reference): [string, FixtureFile] {
  const policy = bundle({
    areas: [
      { name: "app", patterns: ["api:app/**"] },
      { name: "payroll", patterns: ["api:app/payroll/**"], private: true },
      { name: "payroll-ui", patterns: ["api:app/payroll/ui/**"] },
      { name: "secrets", patterns: ["*:**/.env*"], private: true },
      { name: "infra", patterns: ["api:deploy/**"], system: true },
      { name: "deploy-docs", patterns: ["api:deploy/docs/**"] },
      { name: "infra-prod", patterns: ["api:deploy/prod/**"], system: true },
    ],
    roles: {
      dev: role({ write: ["app", "payroll-ui"] }),
      hr: role({ read: ["payroll"], write: ["payroll"] }),
      ops: role({ write: ["infra", "deploy-docs", "infra-prod"] }),
      clerk: role({ write: ["payroll"] }),
    },
    members: { "mem-ana": "dev", "mem-hana": "hr", "mem-omar": "ops", "mem-cal": "clerk" },
  });
  const infraOverride = { id: "ovr-1", area: "infra", actions: ["write"], scope: "until", expires_at: "2026-01-15T13:00:00.000Z" };
  return file(reference, "evaluate-private-and-system", "Private areas, which need a grant to read, and system areas, which nothing can write.", policy, [
    ["read in a private area without a grant is denied", "A private area denies a read unless the role lists it under read.", ask("mem-ana", "read", "app/payroll/salaries.csv")],
    ["read in a private area with a grant is allowed", "A role that lists the private area under read may read it.", ask("mem-hana", "read", "app/payroll/salaries.csv")],
    ["write in a private area with a grant is allowed", "A role that lists the private area under write may write it.", ask("mem-hana", "write", "app/payroll/salaries.csv")],
    ["a write grant does not allow a read in a private area", "A role that lists a private area under write only may not read it. Only a read grant opens a private area for a read.", ask("mem-cal", "read", "app/payroll/salaries.csv")],
    ["a private area denies a write despite a more specific granted area", "A private area that denies wins over every other area, however specific.", ask("mem-ana", "write", "app/payroll/ui/form.tsx")],
    ["a private area denies a read despite a more specific area that is not private", "A private area that denies wins over every other area, however specific.", ask("mem-ana", "read", "app/payroll/ui/form.tsx")],
    ["a granted private area leaves the decision to the most specific area", "A private area that allows takes part in the ranking like any other area.", ask("mem-hana", "read", "app/payroll/ui/form.tsx")],
    ["the first private area that denies is reported", "When two private areas deny, the one listed first in the bundle is reported.", ask("mem-ana", "read", "app/payroll/.env")],
    ["a private area that denies wins over a private area that allows", "A grant on one private area does not open another private area that matches the same path.", ask("mem-hana", "read", "app/payroll/.env")],
    ["read in a system area is allowed", "The system flag restricts only writes.", ask("mem-ana", "read", "deploy/prod.yaml")],
    ["write in a system area is denied despite a role grant", "No role grant makes a system area writable.", ask("mem-omar", "write", "deploy/prod.yaml")],
    ["write in a system area is denied despite an override", "No override makes a system area writable.", ask("mem-omar", "write", "deploy/prod.yaml", { overrides: [infraOverride] })],
    ["a system area denies a write despite a more specific granted area", "A system area wins over every other area for a write.", ask("mem-omar", "write", "deploy/docs/runbook.md")],
    ["the first system area that matches is reported", "When two system areas match a write, the one listed first in the bundle is reported, although the other one is more specific.", ask("mem-omar", "write", "deploy/prod/app.yaml")],
    ["a line feed in a directory name takes a path out of a private area", "`**` does not match a line terminator, so `**/.env*` does not match this path, and the read is allowed by default.", ask("mem-ana", "read", "app2/a\nb/.env")],
    ["a line feed in a file name does not take a path out of a private area", "`*` matches a line terminator, so `**/.env*` matches this path.", ask("mem-ana", "read", "app2/.env\nx")],
  ]);
}

function temporaryGrants(reference: Reference): [string, FixtureFile] {
  const policy = bundle({
    areas: [
      { name: "billing", patterns: ["api:src/billing/**"] },
      { name: "vault", patterns: ["api:vault/**"], private: true },
      { name: "infra", patterns: ["api:deploy/**"], system: true },
      { name: "sealed", patterns: ["api:sealed/**"], private: true, system: true },
    ],
    roles: { dev: role({}) },
    members: { "mem-ana": "dev", "mem-ben": "dev", "mem-cy": "dev", "mem-dan": "ghost" },
    temporary_grants: [
      { member_id: "mem-ana", area: "billing", actions: ["write"], scope: "mission", mission_id: "mis-1" },
      { member_id: "mem-ben", area: "billing", actions: ["write"], scope: "until", expires_at: "2026-01-15T13:00:00Z" },
      { member_id: "mem-ana", area: "vault", actions: ["read"], scope: "until", expires_at: "2026-01-15T14:30:00+02:00" },
      { member_id: "mem-eve", area: "billing", actions: ["write"], scope: "until", expires_at: "2026-01-15T13:00:00Z" },
      { member_id: "mem-ana", area: "infra", actions: ["write"], scope: "until", expires_at: "2026-01-15T13:00:00Z" },
      { member_id: "mem-cy", area: "billing", actions: ["write"], scope: "until", expires_at: "2026-01-15T15:00:00.0019Z" },
      { member_id: "mem-ana", area: "sealed", actions: ["read"], scope: "until", expires_at: "2026-01-15T13:00:00Z" },
      { member_id: "mem-dan", area: "billing", actions: ["write"], scope: "until", expires_at: "2026-01-15T13:00:00Z" },
    ],
  });
  const invoice = "src/billing/invoice.ts";
  return file(reference, "evaluate-temporary-grants", "Temporary grants by mission and by time, expired and not.", policy, [
    ["a mission grant allows in its mission", "A grant with scope mission applies when the query has the same mission.", ask("mem-ana", "write", invoice, { mission_id: "mis-1" })],
    ["a mission grant does not allow in another mission", "A grant with scope mission applies only to its own mission.", ask("mem-ana", "write", invoice, { mission_id: "mis-2" })],
    ["a mission grant does not allow a query without a mission", "A query with no mission matches no mission grant.", ask("mem-ana", "write", invoice)],
    ["a timed grant allows before it expires", "A grant with scope until applies while the time of the query is before expires_at.", ask("mem-ben", "write", invoice)],
    ["a timed grant does not allow at the instant it expires", "The grant ends at expires_at. The comparison is strict.", ask("mem-ben", "write", invoice, { now: "2026-01-15T13:00:00.000Z" })],
    ["a timed grant does not allow after it expires", "An expired grant has no effect.", ask("mem-ben", "write", invoice, { now: "2026-01-15T13:00:00.001Z" })],
    ["a timed grant with a UTC offset allows before it expires", "expires_at is an instant. `14:30:00+02:00` is 12:30 UTC.", ask("mem-ana", "read", "vault/keys.json", { now: "2026-01-15T12:29:59.999Z" })],
    ["a timed grant with a UTC offset does not allow at the instant it expires", "expires_at is an instant. `14:30:00+02:00` is 12:30 UTC.", ask("mem-ana", "read", "vault/keys.json", { now: "2026-01-15T12:30:00.000Z" })],
    ["a timed grant is compared in whole milliseconds", "The digits after the third digit of the fraction are dropped, so `15:00:00.0019` is `15:00:00.001`, and the grant no longer applies at that instant.", ask("mem-cy", "write", invoice, { now: "2026-01-15T15:00:00.001Z" })],
    ["a timed grant with a long fraction allows one millisecond before it expires", "`15:00:00.0019` is `15:00:00.001`, and `15:00:00.000` is before it.", ask("mem-cy", "write", invoice, { now: "2026-01-15T15:00:00.000Z" })],
    ["a temporary grant opens a private area", "A temporary grant can allow a read that a private area would deny.", ask("mem-ana", "read", "vault/keys.json")],
    ["a temporary grant covers only its actions", "A grant for read does not allow a write.", ask("mem-ana", "write", "vault/keys.json")],
    ["a temporary grant covers only its member", "A grant names one member.", ask("mem-cy", "write", invoice, { mission_id: "mis-1" })],
    ["a temporary grant does not apply to a member with no role", "A member that the bundle does not list gets nothing from a temporary grant.", ask("mem-eve", "write", invoice)],
    ["a temporary grant does not open a system area", "No temporary grant makes a system area writable.", ask("mem-ana", "write", "deploy/prod.yaml")],
    ["a temporary grant does not open a private system area for a read", "A temporary grant never applies to a system area, so it cannot open a private system area for a read either.", ask("mem-ana", "read", "sealed/key.pem")],
    ["a temporary grant does not apply to a member whose role is not defined", "A member mapped to a role name that the bundle does not define has no role, so a temporary grant gives the member nothing.", ask("mem-dan", "write", invoice)],
    [
      "a sub-agent gets the mission grant of its parent",
      "For a sub-agent, a mission grant is matched against the mission of the parent.",
      ask("mem-ana", "write", invoice, { agent_session_id: "ses-sub", mission_id: "mis-2", parent: { agent_session_id: "ses-1", mission_id: "mis-1" } }),
    ],
    [
      "a sub-agent does not get a mission grant for its own mission",
      "For a sub-agent, the mission of the sub-agent itself is ignored.",
      ask("mem-ana", "write", invoice, { agent_session_id: "ses-sub", mission_id: "mis-1", parent: { agent_session_id: "ses-1", mission_id: "mis-2" } }),
    ],
    [
      "a sub-agent whose parent has no mission gets no mission grant",
      "The mission of the sub-agent does not stand in for a missing mission of the parent.",
      ask("mem-ana", "write", invoice, { agent_session_id: "ses-sub", mission_id: "mis-1", parent: { agent_session_id: "ses-1" } }),
    ],
  ]);
}

function overrides(reference: Reference): [string, FixtureFile] {
  const policy = bundle({
    areas: [
      { name: "billing", patterns: ["api:src/billing/**"] },
      { name: "vault", patterns: ["api:vault/**"], private: true },
    ],
    roles: { dev: role({}) },
    members: { "mem-ana": "dev", "mem-dan": "ghost" },
  });
  const later = "2026-01-15T13:00:00.000Z";
  const earlier = "2026-01-15T11:00:00.000Z";
  const session = { agent_session_id: "ses-1", mission_id: "mis-1" };
  const override = (fields: Query): Query => ({ id: "ovr-1", area: "billing", actions: ["write"], ...fields });
  const write = (o: Query, extra: Query = session): Query => ask("mem-ana", "write", "src/billing/invoice.ts", { ...extra, overrides: [o] });
  const subAgent = (own: Query, parent: Query): Query => ({ ...own, parent });
  return file(reference, "evaluate-overrides", "Overrides with scope once, mission, and until, and every case in which an override is inactive.", policy, [
    ["without an override the write is denied", "The baseline for this file: the role has no grant on the area.", ask("mem-ana", "write", "src/billing/invoice.ts", session)],
    ["once: allows the session that it names", "An override with scope once applies to the agent session in agent_session_id.", write(override({ scope: "once", agent_session_id: "ses-1" }))],
    ["once: inactive without agent_session_id", "An override with scope once that names no session is inactive.", write(override({ scope: "once" }))],
    ["once: inactive for another session", "An override with scope once applies to one session only.", write(override({ scope: "once", agent_session_id: "ses-2" }))],
    ["once: inactive when the query has no session", "A query with no agent session matches no override with scope once.", write(override({ scope: "once", agent_session_id: "ses-1" }), { mission_id: "mis-1" })],
    ["once: allows before expires_at", "An override with scope once may also carry an expiry.", write(override({ scope: "once", agent_session_id: "ses-1", expires_at: later }))],
    ["once: inactive after expires_at", "expires_at ends an override of any scope.", write(override({ scope: "once", agent_session_id: "ses-1", expires_at: earlier }))],
    ["mission: allows the mission that it names", "An override with scope mission applies to the mission in mission_id.", write(override({ scope: "mission", mission_id: "mis-1" }))],
    ["mission: inactive without mission_id", "An override with scope mission that names no mission is inactive.", write(override({ scope: "mission" }))],
    ["mission: inactive for another mission", "An override with scope mission applies to one mission only.", write(override({ scope: "mission", mission_id: "mis-2" }))],
    ["mission: inactive when the query has no mission", "A query with no mission matches no override with scope mission.", write(override({ scope: "mission", mission_id: "mis-1" }), { agent_session_id: "ses-1" })],
    ["until: allows before expires_at", "An override with scope until applies while the time of the query is before expires_at.", write(override({ scope: "until", expires_at: later }))],
    ["until: inactive without expires_at", "An override with scope until that has no expiry is inactive.", write(override({ scope: "until" }))],
    ["until: inactive at the instant of expires_at", "The override ends at expires_at. The comparison is strict.", write(override({ scope: "until", expires_at: NOON }))],
    ["until: inactive after expires_at", "An expired override has no effect.", write(override({ scope: "until", expires_at: earlier }))],
    ["until: inactive when expires_at is not a timestamp", "An expiry that cannot be read as a time makes the override inactive.", write(override({ scope: "until", expires_at: "not a timestamp" }))],
    ["until: expires_at is compared in whole milliseconds", "The digits after the third digit of the fraction are dropped, so `12:00:00.0005` is `12:00:00.000`, which is not after the time of the query.", write(override({ scope: "until", expires_at: "2026-01-15T12:00:00.0005Z" }))],
    ["until: the digits after the third are dropped, not rounded", "`12:00:00.0019` is `12:00:00.001`, which is not after a query at `12:00:00.001`.", write(override({ scope: "until", expires_at: "2026-01-15T12:00:00.0019Z" }), { ...session, now: "2026-01-15T12:00:00.001Z" })],
    ["until: allows one millisecond before a truncated expires_at", "`12:00:00.0019` is `12:00:00.001`, and a query at `12:00:00.000` is before it.", write(override({ scope: "until", expires_at: "2026-01-15T12:00:00.0019Z" }))],
    ["until: the time of the query is compared in whole milliseconds", "`now` loses the digits after the third too, so `12:59:59.9999` is `12:59:59.999`, which is before `13:00:00.000`.", write(override({ scope: "until", expires_at: later }), { ...session, now: "2026-01-15T12:59:59.9999Z" })],
    ["until: inactive when expires_at has the second 60", "A leap second is not a timestamp of this contract, so the override is inactive, although the time is months after the query.", write(override({ scope: "until", expires_at: "2026-06-30T23:59:60Z" }))],
    ["until: inactive for another session when it names a session", "agent_session_id restricts an override of any scope.", write(override({ scope: "until", expires_at: later, agent_session_id: "ses-2" }))],
    ["until: inactive for another mission when it names a mission", "mission_id restricts an override of any scope.", write(override({ scope: "until", expires_at: later, mission_id: "mis-2" }))],
    ["an override for another area has no effect", "An override opens only the area that it names.", write(override({ scope: "until", expires_at: later, area: "vault" }))],
    ["an override for another action has no effect", "An override allows only the actions that it lists.", write(override({ scope: "until", expires_at: later, actions: ["read"] }))],
    ["an override opens a private area for a read", "An override can allow a read that a private area would deny.", ask("mem-ana", "read", "vault/keys.json", { ...session, overrides: [override({ scope: "until", expires_at: later, area: "vault", actions: ["read"] })] })],
    ["an override does not apply to a member with no role", "A member that the bundle does not list gets nothing from an override.", ask("mem-eve", "write", "src/billing/invoice.ts", { ...session, overrides: [override({ scope: "until", expires_at: later })] })],
    [
      "an override does not apply to a member whose role is not defined",
      "A member mapped to a role name that the bundle does not define has no role, so an override gives the member nothing.",
      ask("mem-dan", "write", "src/billing/invoice.ts", { ...session, overrides: [override({ scope: "until", expires_at: later })] }),
    ],
    ["one active override among inactive ones allows", "Any one active override is enough.", ask("mem-ana", "write", "src/billing/invoice.ts", { ...session, overrides: [override({ id: "ovr-0", scope: "until" }), override({ scope: "mission", mission_id: "mis-1" })] })],
    [
      "a sub-agent gets the once override of its parent session",
      "For a sub-agent, an override is matched against the session of the parent.",
      write(override({ scope: "once", agent_session_id: "ses-1" }), subAgent({ agent_session_id: "ses-sub", mission_id: "mis-1" }, { agent_session_id: "ses-1", mission_id: "mis-1" })),
    ],
    [
      "a sub-agent does not get an override for its own session",
      "An override that names the session of the sub-agent is inactive, so a sub-agent gets at most the grants of its parent.",
      write(override({ scope: "once", agent_session_id: "ses-sub" }), subAgent({ agent_session_id: "ses-sub", mission_id: "mis-1" }, { agent_session_id: "ses-1", mission_id: "mis-1" })),
    ],
    [
      "a sub-agent gets the mission override of its parent mission",
      "For a sub-agent, an override is matched against the mission of the parent.",
      write(override({ scope: "mission", mission_id: "mis-1" }), subAgent({ agent_session_id: "ses-sub", mission_id: "mis-2" }, { agent_session_id: "ses-1", mission_id: "mis-1" })),
    ],
    [
      "a sub-agent does not get an override for its own mission",
      "For a sub-agent, the mission of the sub-agent itself is ignored.",
      write(override({ scope: "mission", mission_id: "mis-2" }), subAgent({ agent_session_id: "ses-sub", mission_id: "mis-2" }, { agent_session_id: "ses-1", mission_id: "mis-1" })),
    ],
  ]);
}

function exec(reference: Reference): [string, FixtureFile] {
  const policy = bundle({
    areas: [{ name: "everything", patterns: ["*:**"] }],
    roles: { dev: role({ exec: ["npm test", "git status", "make"] }), reviewer: role({}) },
    members: { "mem-ana": "dev", "mem-ben": "reviewer" },
  });
  const execOverride = { id: "ovr-1", area: "everything", actions: ["exec"], scope: "until", expires_at: "2026-01-15T13:00:00.000Z" };
  return file(reference, "evaluate-exec", "Command grants: exact match, prefix match, and commands that contain shell syntax.", policy, [
    ["a command equal to a grant is allowed", "A command matches a grant that is the same string.", run("mem-ana", "npm test")],
    ["a command that continues a grant after a space is allowed", "A command matches a grant that it starts with, if white space follows the grant.", run("mem-ana", "npm test -- --watch")],
    ["a command that continues a grant after a tab is allowed", "Any white space character ends the prefix, not only a space.", run("mem-ana", "make\tbuild")],
    ["a command that continues a grant after a line tabulation is allowed", "U+000B is white space.", run("mem-ana", "npm test\u000bx")],
    ["a command that continues a grant after a no-break space is allowed", "U+00A0 is white space.", run("mem-ana", "npm test\u00a0x")],
    ["a command that continues a grant after a line separator is allowed", "U+2028 is white space, and it is not one of the characters that deny a command.", run("mem-ana", "npm test\u2028x")],
    ["a command that continues a grant after a zero width no-break space is allowed", "U+FEFF is white space.", run("mem-ana", "npm test\ufeffx")],
    ["a command that continues a grant after a next line character is denied", "U+0085 is not white space in this contract.", run("mem-ana", "npm test\u0085x")],
    ["a command that continues a grant after a zero width space is denied", "U+200B is not white space in this contract.", run("mem-ana", "npm test\u200bx")],
    ["white space outside ASCII around the command is ignored", "The command is trimmed of every white space character, here U+FEFF and U+00A0.", run("mem-ana", "\ufeffnpm test\u00a0")],
    ["a next line character around the command is not ignored", "U+0085 is not white space, so it stays in the command.", run("mem-ana", "\u0085npm test")],
    ["a command that continues a grant inside a word is denied", "`npm testing` does not match the grant `npm test`.", run("mem-ana", "npm testing")],
    ["a command that starts with a granted word but is another word is denied", "`makefile` does not match the grant `make`.", run("mem-ana", "makefile")],
    ["white space around the command is ignored", "The command is trimmed before it is compared.", run("mem-ana", "  npm test  ")],
    ["white space inside the command is not normalized", "`npm  test` with two spaces does not match the grant `npm test`.", run("mem-ana", "npm  test")],
    ["a command is compared with case", "Command matching is case-sensitive, unlike path matching.", run("mem-ana", "NPM test")],
    ["a command shorter than every grant is denied", "A grant is a prefix of the command, never the reverse.", run("mem-ana", "git")],
    ["a command with no matching grant is denied", "A command is denied by default.", run("mem-ana", "rm -rf build")],
    ["a command with ; is denied", "A command that contains `;` is denied before any grant is tried.", run("mem-ana", "npm test; rm -rf build")],
    ["a command with && is denied", "A command that contains `&` is denied before any grant is tried.", run("mem-ana", "npm test && npm run deploy")],
    ["a command with | is denied", "A command that contains `|` is denied before any grant is tried.", run("mem-ana", "npm test | tee out.log")],
    ["a command with > is denied", "A command that contains `>` is denied before any grant is tried.", run("mem-ana", "npm test > out.log")],
    ["a command with < is denied", "A command that contains `<` is denied before any grant is tried.", run("mem-ana", "npm test < input.txt")],
    ["a command with a backquote is denied", "A command that contains a backquote is denied before any grant is tried.", run("mem-ana", "npm test `whoami`")],
    ["a command with $( is denied", "A command that contains `$(` is denied before any grant is tried.", run("mem-ana", "npm test $(whoami)")],
    ["a command with a line feed is denied", "A command that contains a line feed is denied before any grant is tried.", run("mem-ana", "npm test\nrm -rf build")],
    ["a command with a carriage return is denied", "A command that contains a carriage return is denied before any grant is tried.", run("mem-ana", "npm test\rrm -rf build")],
    ["shell syntax inside quotes is still denied", "The check does not parse quotes.", run("mem-ana", 'npm test "a;b"')],
    ["a command with a $ variable is allowed", "`$` alone is not treated as shell syntax. Only `$(` is.", run("mem-ana", "npm test $HOME")],
    ["a role with an empty exec list is denied", "A role with no command grants can run nothing.", run("mem-ben", "npm test")],
    ["a member with no role is denied", "A member that the bundle does not list has no command grants.", run("mem-eve", "npm test")],
    ["an override does not allow a command", "`exec` ignores overrides and areas.", run("mem-ben", "npm test", { overrides: [execOverride], agent_session_id: "ses-1" })],
  ]);
}

function gitPush(reference: Reference): [string, FixtureFile] {
  const policy = bundle({
    areas: [],
    roles: { dev: role({ push: ["api"] }), lead: role({ push: ["*"] }), reviewer: role({}) },
    members: { "mem-ana": "dev", "mem-lee": "lead", "mem-ben": "reviewer" },
  });
  const push = (member: string, repo: string): Query => ask(member, "git.push", "refs/heads/main", { repo });
  return file(reference, "evaluate-git-push", "Push grants by repository.", policy, [
    ["push to a granted repository is allowed", "A role may push to the repositories that it lists under push.", push("mem-ana", "api")],
    ["push to another repository is denied", "A push is denied by default.", push("mem-ana", "web")],
    ["push with * is allowed for every repository", "`*` under push means every repository, also one that the bundle does not list.", push("mem-lee", "tools")],
    ["push by a role with no push list is denied", "`push` is optional in a role, and its absence grants nothing.", push("mem-ben", "api")],
    ["push by a member with no role is denied", "A member that the bundle does not list has no push grants.", push("mem-eve", "api")],
    ["the repository of a push is compared with case", "Push matching is case-sensitive, unlike the repository part of an area pattern.", push("mem-ana", "API")],
  ]);
}

function paths(reference: Reference): [string, FixtureFile] {
  const policy = bundle({
    areas: [
      { name: "src", patterns: ["api:src/**"] },
      { name: "drive", patterns: ["api:c:/**", "api:c:src/**"] },
    ],
    roles: { dev: role({ write: ["src", "drive"] }) },
    members: { "mem-ana": "dev" },
  });
  const write = (path: string): Query => ask("mem-ana", "write", path);
  return file(reference, "evaluate-paths", "How a path is made canonical before it is matched, and which paths are denied outright.", policy, [
    ["a plain relative path matches", "The baseline for this file.", write("src/a.ts")],
    ["a leading ./ is removed", "`.` segments are dropped.", write("./src/a.ts")],
    ["a . segment inside the path is removed", "`.` segments are dropped.", write("src/./a.ts")],
    ["a .. segment removes the segment before it", "`..` is resolved before the path is matched.", write("src/sub/../a.ts")],
    ["a path that enters the area through .. matches", "The area is chosen from the resolved path.", write("docs/../src/a.ts")],
    ["a path that leaves the area through .. does not match", "The area is chosen from the resolved path, not from its first segment.", write("src/../README.md")],
    ["a path that starts with .. is denied", "A path that escapes the project root is denied.", write("../src/a.ts")],
    ["a path that escapes the root after entering it is denied", "The escape is detected at any depth.", write("src/../../src/a.ts")],
    ["an absolute path is denied", "A path that starts with `/` is denied.", write("/src/a.ts")],
    ["a backslash is a separator", "`\\` is replaced by `/` before anything else.", write("src\\a.ts")],
    ["a path that starts with a backslash is denied", "After the replacement the path is absolute.", write("\\src\\a.ts")],
    ["a path with a drive letter is denied", "A path that starts with a letter and `:` is denied, although the area `drive`, which the role may write, matches the path.", write("C:/src/a.ts")],
    ["a drive letter without a slash is denied", "The drive check needs only the letter and the colon. The area `drive` matches this path too.", write("c:src/a.ts")],
    ["a read of a path with a drive letter is denied", "The drive check comes before the default for reads, which would allow the read.", ask("mem-ana", "read", "C:/src/a.ts")],
    ["a read of a drive letter without a slash is denied", "The drive check comes before the default for reads, which would allow the read.", ask("mem-ana", "read", "c:src/a.ts")],
    ["a letter outside ASCII before a colon is not a drive letter", "The drive check takes only the ASCII letters. No area matches `é:src/a.ts`, so the read is allowed by default.", ask("mem-ana", "read", "é:src/a.ts")],
    ["an empty segment is dropped", "`//` is the same as `/`.", write("src//a.ts")],
    ["a trailing slash is dropped", "The trailing empty segment is dropped.", write("src/sub/")],
    ["the path is compared without case", "Path matching is case-insensitive. The claims dialect in the Room contract is case-sensitive.", write("SRC/A.TS")],
    ["a read of a path that escapes the root is denied", "The escape is denied before the default for reads applies.", ask("mem-ana", "read", "../secrets.txt")],
    ["a read of an absolute path is denied", "An absolute path is denied before the default for reads applies.", ask("mem-ana", "read", "/etc/passwd")],
  ]);
}

function globs(reference: Reference): [string, FixtureFile] {
  const patterns: [area: string, pattern: string][] = [
    ["dir", "api:assets/**"],
    ["star", "api:logs/*.log"],
    ["middle", "api:pkg/**/test/*.ts"],
    ["leading", "api:**/.env*"],
    ["question", "api:data/file?.csv"],
    ["class", "api:img/[abc].png"],
    ["range", "api:rel/v[0-9]/notes.md"],
    ["negated", "api:tmp/[!abc].txt"],
    ["caret", "api:tmp2/[^abc].txt"],
    ["unclosed", "api:odd/[x"],
    ["inner-stars", "api:gen/a**b"],
    ["trailing-stars", "api:build**"],
    ["normalized", "api:./norm//x/**"],
    ["literal", "api:lit/a+b(1).txt"],
    ["negated-empty", "api:neg/a[!]b"],
    ["caret-empty", "api:neg2/a[^]b"],
    ["negated-then", "api:negx/[!]x]"],
    ["bracket-first", "api:first/[]x]"],
    ["bracket-only", "api:first2/[]]"],
    ["negated-slash", "api:t[!a]x"],
    ["range-slash", "api:r[.-0]x"],
    ["question-pair", "api:q/?"],
    ["triple-dir", "api:tri/***/x"],
    ["triple-inner", "api:tri2/a***b"],
    ["range-upper", "api:rc/[@-Z]x"],
    ["range-mixed", "api:rm/[A-_]x"],
  ];
  const policy = bundle({
    areas: patterns.map(([name, pattern]) => ({ name, patterns: [pattern] })),
    roles: { dev: role({ write: patterns.map(([name]) => name) }) },
    members: { "mem-ana": "dev" },
  });
  const write = (path: string): Query => ask("mem-ana", "write", path);
  return file(reference, "evaluate-globs", "The policy glob dialect. The role may write every area, so a result of allow with an area means that the pattern of that area matched, and deny means that no pattern matched.", policy, [
    ["dir/** matches a file below the directory", "`**` matches any number of segments.", write("assets/img/logo.png")],
    ["dir/** matches the directory itself", "A pattern that ends in `/**` also matches the path before `/**`.", write("assets")],
    ["dir/** matches the directory itself without case", "The directory rule is case-insensitive like the rest.", write("ASSETS")],
    ["dir/** does not match a longer name", "The directory name must match as a whole segment.", write("assetsx")],
    ["* matches inside one segment", "`*` matches any run of characters except `/`.", write("logs/app.log")],
    ["* matches an empty run", "`*` may match nothing.", write("logs/.log")],
    ["* does not cross a slash", "`*` never matches `/`.", write("logs/sub/app.log")],
    ["**/ in the middle matches zero directories", "`a/**/b` matches `a/b`.", write("pkg/test/a.ts")],
    ["**/ in the middle matches several directories", "`a/**/b` matches `a/x/y/b`.", write("pkg/x/y/test/a.ts")],
    ["**/ in the middle needs a whole segment", "`pkg/**/test` does not match `pkg/xtest`.", write("pkg/xtest/a.ts")],
    ["a leading **/ matches at the root", "`**/name` matches `name`.", write(".env")],
    ["a leading **/ matches at any depth", "`**/name` matches `a/b/name`.", write("a/b/.env.local")],
    ["a leading **/ still anchors the segment", "`**/.env*` does not match `x.env`.", write("a/b/x.env")],
    ["? matches one character", "`?` matches exactly one character.", write("data/file1.csv")],
    ["? does not match zero characters", "`?` matches exactly one character.", write("data/file.csv")],
    ["? does not match two characters", "`?` matches exactly one character.", write("data/file12.csv")],
    ["a class matches a listed character", "`[abc]` matches one of the listed characters.", write("img/a.png")],
    ["a class matches without case", "A class is case-insensitive like the rest of the pattern.", write("img/B.png")],
    ["a class does not match another character", "`[abc]` matches only the listed characters.", write("img/d.png")],
    ["a class with a range matches inside the range", "`[0-9]` matches one digit.", write("rel/v7/notes.md")],
    ["a class with a range does not match outside the range", "`[0-9]` matches one digit.", write("rel/vx/notes.md")],
    ["a class negated with ! matches another character", "`[!abc]` matches one character that is not listed.", write("tmp/d.txt")],
    ["a class negated with ! does not match a listed character", "`[!abc]` matches one character that is not listed.", write("tmp/a.txt")],
    ["a class negated with ^ matches another character", "`^` at the start of a class negates it, as `!` does.", write("tmp2/d.txt")],
    ["a class negated with ^ does not match a listed character", "`^` at the start of a class negates it, as `!` does.", write("tmp2/a.txt")],
    ["a [ with no ] is a literal character", "A class that is not closed matches the character `[` itself.", write("odd/[x")],
    ["[!] matches a slash", "The `!` counts as the first character of the class, so `[!]` is a negated class with no members, which matches any one character.", write("neg/a/b")],
    ["[!] matches a line feed", "A negated class with no members matches a line terminator too.", write("neg/a\nb")],
    ["[!] does not match zero characters", "A class matches exactly one character.", write("neg/ab")],
    ["[^] matches a slash", "`[^]` is the same as `[!]`.", write("neg2/a/b")],
    ["a negated class with members matches a slash", "`[!a]` matches every character that is not `a`, `/` included, so `t[!a]x` crosses a directory boundary.", write("t/x")],
    ["a class whose range holds the slash matches a slash", "`[.-0]` is the range from `.` to `0`, which holds `/`.", write("r/x")],
    ["? does not match one character outside the Basic Multilingual Plane", "`?` matches one UTF-16 code unit, and U+1F600 is two.", write("q/\u{1f600}")],
    ["a negated class does not match one character outside the Basic Multilingual Plane", "A class matches one UTF-16 code unit, and U+1F600 is two. The sixth open question of the README records the behavior.", write("t\u{1f600}x")],
    ["*** before a slash matches zero directories", "A run of three or more `*` is the same as `**`, so `tri/***/x` matches `tri/x`.", write("tri/x")],
    ["*** before a slash matches several directories", "A run of three or more `*` is the same as `**`.", write("tri/a/b/x")],
    ["*** inside a segment crosses slashes", "A run of three or more `*` is the same as `**`, so `a***b` matches across `/`.", write("tri2/a/x/b")],
    ["a range matches the other case of a letter that it holds", "`[@-Z]` holds `A`, so it matches `a` without case.", write("rc/ax")],
    ["a range does not match a character between the two cases", "`[@-Z]` does not hold `_`, and `_` has no other case. The ends of a range are not folded, so `[@-Z]` is not `[@-z]`.", write("rc/_x")],
    ["a range whose ends differ in case is in order", "`[A-_]` runs from U+0041 to U+005F, so it is in order, and it matches `a` because it holds `A`.", write("rm/ax")],
    ["[!]x] is [!] followed by x and ]", "The class ends at the first `]` after its first character, so the rest of the glob is literal.", write("negx/ax]")],
    ["[!]x] does not match one character", "`[!]x]` needs three characters.", write("negx/a")],
    ["a class whose first character is ] does not match that character", "The reference reads `[]` as a class that matches nothing, so the glob matches no path. The second open question of the README records the behavior.", write("first/]")],
    ["a class whose first character is ] does not match its other member", "The reference reads `[]` as a class that matches nothing, so the glob matches no path. The second open question of the README records the behavior.", write("first/x")],
    ["a class whose first character is ] does not match its own text", "The reference reads `[]` as a class that matches nothing, so the glob matches no path. The second open question of the README records the behavior.", write("first/]x]")],
    ["[]] does not match ]", "The reference reads `[]` as a class that matches nothing, so the glob matches no path. The second open question of the README records the behavior.", write("first2/]")],
    ["** inside a segment crosses slashes", "`**` that is not a whole segment matches any run of characters, including `/`.", write("gen/a/x/y/b")],
    ["** at the end of a segment crosses slashes", "`build**` matches `build-out/x`.", write("build-out/x/y.js")],
    ["a pattern is normalized like a path", "A leading `./` and a repeated `/` in a pattern are removed.", write("norm/x/y.ts")],
    ["other punctuation in a pattern is literal", "`+`, `(`, `)`, and `.` match themselves.", write("lit/a+b(1).txt")],
    ["a . in a pattern does not match any character", "`.` is not a wildcard.", write("lit/a+b(1)xtxt")],
    ["** does not match a line feed", "`**` matches any run of characters except the four line terminators.", write("assets/a\nb.png")],
    ["** does not match a carriage return", "`**` matches any run of characters except the four line terminators.", write("assets/a\rb.png")],
    ["** does not match U+2028", "`**` matches any run of characters except the four line terminators.", write("assets/a\u2028b.png")],
    ["** does not match U+2029", "`**` matches any run of characters except the four line terminators.", write("assets/a\u2029b.png")],
    ["** matches U+0085", "The next line character is not one of the four line terminators.", write("assets/a\u0085b.png")],
    ["** inside a segment does not match a line feed", "The rule for line terminators holds for `**` in every position.", write("gen/a\nb")],
    ["**/ in the middle does not cross a directory with a line feed in its name", "The directories that `**/` matches hold no line terminator.", write("pkg/a\nb/test/a.ts")],
    ["a leading **/ does not cross a directory with a line feed in its name", "The directories that `**/` matches hold no line terminator.", write("a\nb/.env")],
    ["* matches a line feed", "Only `**` excludes the line terminators. `*` excludes only `/`.", write("logs/a\nb.log")],
    ["? matches a line feed", "Only `**` excludes the line terminators. `?` excludes only `/`.", write("data/file\n.csv")],
    ["a negated class matches a line feed", "Only `**` excludes the line terminators.", write("tmp/\n.txt")],
  ]);
}

export function policyEvaluateFixtures(reference: Reference): [string, FixtureFile][] {
  return [defaultsAndRoles, specificity, privateAndSystem, temporaryGrants, overrides, exec, gitPush, paths, globs].map((build) => build(reference));
}
