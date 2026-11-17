# spec/policy

This contract defines the policy bundle, the override, the evaluation of one action against a bundle, and the signed form in which a bundle reaches a daemon. The contract is at version 1.

The field descriptions are in the schemas. This page gives the rules that a schema cannot express.

## Files

| File | Content |
|---|---|
| `v1/bundle.schema.json` | The policy of one project: repositories, areas, roles, members, and temporary grants. |
| `v1/override.schema.json` | A grant that a lead gives to open one area for an agent. |
| `v1/eval-query.schema.json` | The request that the evaluator answers. |
| `v1/eval-result.schema.json` | The answer of the evaluator. |
| `v1/signed-bundle.schema.json` | The message that carries a bundle and its signature. |
| `v1/jws-protected-header.schema.json` | The protected header of the signature. |
| `v1/jwks.schema.json` | The key set that verifies the signature. |
| `v1/examples/` | Valid instances of each schema. The `eval-result` examples are the results of the `eval-query` examples with the same name, evaluated against `examples/bundle/full.json`. |
| `v1/fixtures/evaluate-*.json` | Evaluation cases, one file per group of rules. |
| `v1/fixtures/validate-bundle.json`, `v1/fixtures/validate-override.json` | Valid and invalid bundles and overrides. |
| `v1/fixtures/verify-signed-bundle.json` | Signed bundles that verify, and signed bundles that must not. |
| `v1/fixtures/verify-signed-bundle-key-selection.json` | Signed bundles checked against a key set with several keys, for the rule that chooses the key. |

## Evaluation

The evaluator takes a bundle and a query, and returns a result. The evaluator reads no file, no network, and no clock. The time comes from `now` in the query.

The result has a `decision`, which is `allow` or `deny`. It has an `area` when an area decided the result. It has a `rule`, a sentence for a person. The `decision` and the `area` are part of the contract. The wording of `rule` is not, and an implementation need not reproduce it.

A member has a role when `members` maps the member id to a role name and `roles` defines that name. In every other case the member has no grants. A member id and a role name are compared as exact strings with the keys of `members` and of `roles`. No name has a special meaning.

### exec

1. If `command` contains `;`, `&`, `|`, `<`, `>`, a backquote, a line feed, a carriage return, or the two characters `$(`, the result is deny. The check does not parse quotes.
2. Remove the white space at both ends of `command`.
3. If the member has a role, and `command` matches one string in the `exec` list of the role, the result is allow. The command matches a string if the two are equal, or if the command starts with the string and the next character is white space.
4. In every other case the result is deny.

White space is the set of code points that [the general page](../README.md#white-space) defines. Step 1 denies a line feed and a carriage return before step 2 can remove them. It does not deny the other white space, so U+2028 after a granted string is allowed.

The comparison is case-sensitive. The result has no `area`. Areas, temporary grants, and overrides have no effect on `exec`.

### git.push

The result is allow if the member has a role and the `push` list of the role contains `*` or a string equal to `repo`. In every other case the result is deny. The comparison is case-sensitive. The result has no `area`. Areas, temporary grants, and overrides have no effect on `git.push`.

### read and write

**Step 1. Make the path canonical.**

1. Replace every `\` with `/`.
2. If the path starts with `/`, or starts with one ASCII letter followed by `:`, the result is deny with no `area`.
3. Split the path at `/`. Drop each empty segment and each `.` segment. A `..` segment removes the segment before it. If there is no segment before it, the result is deny with no `area`.
4. Join the segments with `/`.

**Step 2. Find the areas that match.** Take the areas in the order of the bundle. For each pattern of an area:

1. Split the pattern at its first `:` into a repository and a glob. A pattern with no `:` has the repository `*`.
2. The pattern applies if the repository is `*`, or if it equals `repo` in the query when both are compared without case.
3. Normalize the glob: replace every `\` with `/`, remove every leading `./`, and replace each run of `/` with one `/`.
4. The pattern matches if the glob matches the canonical path under [the glob rules](#the-policy-glob-dialect). A glob that ends in `/**` also matches the path that equals the part before `/**`, compared without case.

An area matches if one of its patterns matches. The specificity of a pattern is a pair: the number of characters in the normalized glob before the first `*`, `?`, or `[`, and then the number of segments of the normalized glob when it is split at `/`. One pattern is more specific than another if its first number is greater, or if the first numbers are equal and its second number is greater. An area has the specificity of its most specific matching pattern.

**Step 3. Apply the system areas.** If the action is `write` and a matching area has `system: true`, the result is deny. The `area` is the first such area in the order of the bundle.

**Step 4. Decide each matching area.** An area allows the action if one of these holds.

1. The action is `write`, and the role of the member lists the area under `write`.
2. The action is `read`, and the area is not private or the role of the member lists the area under `read`.
3. The member has a role, the area is not a system area, and a temporary grant of the bundle applies.
4. The member has a role, the area is not a system area, and an override of the query applies.

In every other case the area denies the action.

**Step 5. Apply the private areas.** If a matching private area denies the action, the result is deny. The `area` is the first such area in the order of the bundle. A more specific area cannot change this result.

**Step 6. Choose the area that decides.** If no area matched, a `read` is allowed and a `write` is denied, with no `area`. If areas matched, go through them in the order of the bundle and keep one as the winner. A later area replaces the winner if it is more specific, or if it has the same specificity, the winner allows, and the later area denies. The result is the decision of the winner, and `area` is its name.

### The holder of grants

Temporary grants and overrides are matched against a session and a mission. For a query with no `parent`, they are `agent_session_id` and `mission_id` of the query. For a query with a `parent`, they are `agent_session_id` and `mission_id` of the parent, and the values of the query itself are ignored. A sub-agent therefore gets the grants of its parent and no others.

### Temporary grants

A temporary grant applies to an area and an action if all of these hold.

- `member_id` equals `member` in the query.
- `area` equals the name of the area.
- `actions` contains the action.
- For the scope `mission`: the holder has a mission, and it equals `mission_id`.
- For the scope `until`: `now` is before `expires_at`, under [the rule for times](#timestamps). At the instant of `expires_at` the grant no longer applies.

### Overrides

An override applies to an area and an action if all of these hold. An override that fails one condition is inactive.

- `area` equals the name of the area.
- `actions` contains the action.
- If the override has `agent_session_id`, the holder has that session.
- If the override has `mission_id`, the holder has that mission.
- For the scope `once`: the override has `agent_session_id`.
- For the scope `mission`: the override has `mission_id`.
- For the scope `until`: the override has `expires_at`.
- If the override has `expires_at`: the value is a timestamp, and `now` is before it, under [the rule for times](#timestamps).

The evaluator does not record that an override with the scope `once` was used. The caller removes it after the first evaluation that it allows.

### Timestamps

A timestamp is a string that matches the `pattern` of `now` in `eval-query.schema.json`. The pattern of `expires_at` in a temporary grant is the same one. The form is a date, `T`, a time with seconds, an optional fraction of a second, and then `Z` or an offset such as `+01:00`. Two examples are `2026-01-15T12:00:00Z` and `2026-01-15T12:00:00.250+01:00`.

The form is narrower than RFC 3339 in two ways.

- `T` and `Z` are uppercase.
- The second is from `00` to `59`. A leap second, such as `23:59:60`, is not a timestamp.

As in RFC 3339, the date exists in the calendar, so `2026-02-30` is not a timestamp.

A schema checks `now` and the `expires_at` of a temporary grant, so both are always timestamps. The schema of an override accepts any string as `expires_at`, and an override whose `expires_at` is not a timestamp is inactive.

To compare two times, read each one as a whole number of milliseconds since `1970-01-01T00:00:00Z`. Drop the digits of the fraction after the third digit. Do not round. `12:00:00.0005Z` and `12:00:00.0009Z` are therefore both `12:00:00.000Z`. One time is before another if its number is smaller.

### The policy glob dialect

A glob matches a path only if it matches the whole path. Letters are compared without case. A letter is in a class range if the range holds the letter or the same letter in the other case. The ends of the range keep their case, so `[@-Z]` matches `a` and does not match `_`, and `[A-_]` is a range in order that matches `a` and `_`. The sixth open question below gives the rule for letters outside ASCII, and what one character means.

| Glob | Matches |
|---|---|
| `**` at the start of the glob or after `/`, and followed by `/` | Zero or more whole directories, none of which has a line terminator in its name. `a/**/b` matches `a/b` and `a/x/y/b`. |
| `**` in any other position | Any run of characters, including `/`, that holds no line terminator. |
| `*` | Any run of characters except `/`, including an empty run. |
| `?` | One character except `/`. |
| `[abc]`, `[a-z]` | One character of the class, including `/` if the class lists it or a range holds it, as `[.-0]` does. |
| `[!abc]`, `[^abc]` | One character that is not in the class, including `/`. |
| `[!]`, `[^]` | Any one character, including `/` and a line terminator. |
| `[` that starts no class | The character `[`. |
| Any other character | That character. |

A run of three or more `*` is the same as `**`, in both of the rows above. So `a/***/b` matches `a/b`, and `a***b` matches `a/x/b`.

A class holds at least one character. It starts at `[` and ends at the first `]` after its first character. The `!` or `^` that negates a class counts as its first character, so `[!]` and `[^]` are negated classes with no members. `[!]x]` is the class `[!]` followed by the characters `x` and `]`. A `\` in a glob is already `/` when the glob is matched, so `[\]` is the class of `/`. A class whose first character is `]`, such as `[]a]`, does not follow the rows above. The second open question below gives what the reference does with it.

The line terminators are four characters: the line feed U+000A, the carriage return U+000D, U+2028, and U+2029. Only `**` excludes them. `*`, `?`, a negated class, and a literal character match a line terminator like any other character. A path with a line terminator in the part that a `**` must cover therefore matches no such glob. `src/**` does not match `src/a` followed by a line feed and `b`, and `src/*` does. The first open question below gives the consequence for a private area.

## The signed bundle

The hub sends a bundle as a message with the fields `version`, `bundle`, and `signature`.

`signature` is a compact JWS (RFC 7515) with these properties.

- The algorithm is ES256: ECDSA with the curve P-256 and SHA-256.
- The protected header has `alg` with the value `ES256`, `typ` with the value `mp-policy+jws`, and `kid` with the id of the signing key.
- The payload is the UTF-8 bytes of the [canonical JSON](../canonical-json/README.md) of the bundle.

To verify a signed bundle, do these steps. A failure in one step means that the bundle is rejected. The steps follow the verifier of the reference, which accepts some tokens that RFC 7515 rejects. The twelfth open question below lists them.

1. Split the token at `.`. It must have exactly three segments: the header, the payload, and the signature. White space after the token, such as a line feed, belongs to the signature segment.
2. Read the header. Decode the header segment under [the base64url rule](#the-base64url-rule). Decode the bytes as UTF-8, with a byte order mark at the start removed. An invalid sequence fails. Parse the text as JSON. It must be an object. When a key repeats, the last member counts.
3. Check the header.
    - `alg` is `ES256`.
    - `typ` is `mp-policy+jws`.
    - `crit` is absent, or it is a list that is not empty and whose every member is the string `b64`. A `crit` that names another parameter fails, and so does an empty `crit`.
    - If `crit` is present, `b64` is `true` or `false`. Without `crit`, `b64` has no effect.
4. Choose the key. A key of the set is a candidate if all of these hold:
    - `kty` is `EC`, and `crv` is `P-256`.
    - If the header has a `kid`, that `kid` is a string, and the key has a `kid` that is the same string. A `kid` of another type, such as the number `1`, matches no key.
    - The key has no `alg`, or its `alg` is `ES256`.
    - The key has no `use`, or its `use` is `sig`.
    - The key has no `key_ops`, or `key_ops` is a list of distinct strings that holds `verify`.
    - The key has no `ext`, or its `ext` is a boolean.

    If the set has no candidate, or more than one, the bundle is rejected. So a header with no `kid` fails when two keys of the set are candidates, and two keys with the same `kid` make every bundle signed under that `kid` fail, even if one of them is the signing key. The candidate must be a public key. If it has `key_ops`, the list must hold no operation other than `verify`, so a candidate whose `key_ops` is `["verify", "sign"]` fails. The fixture `verify-signed-bundle-key-selection` pins the rule.
5. Verify the signature. The signing input is the ASCII text of the header segment, `.`, and the payload segment, exactly as the token holds them. Decode the signature segment under the base64url rule. It must be a valid ES256 signature of the signing input under the key: 64 bytes, `r` and then `s`.
6. Get the payload. If `crit` is present and `b64` is `false`, the payload is the payload segment itself, as RFC 7797 defines, and it must be ASCII. Such a payload cannot hold `.`. In every other case, decode the payload segment under the base64url rule.
7. Decode the payload as UTF-8, as the UTF-8 decode of the WHATWG Encoding Standard does: remove a byte order mark at the start, and replace each invalid sequence with U+FFFD. Parse the text as JSON. When a key repeats, the last member counts, as it does in [the input of canonical JSON](../canonical-json/README.md#the-input). Validate the result against `bundle.schema.json`.

The verified bundle is the payload. The fields `bundle` and `version` of the message are copies for display, and a bundle verifier does not trust them.

The key set is a JSON Web Key Set (RFC 7517) with the public keys of the hub.

Verification does not look at the version of the bundle. A daemon that holds a bundle refuses a verified bundle with a lower `version` for the same `project_id`. That rule belongs to the daemon, not to this contract.

### The base64url rule

Replace each `-` with `+` and each `_` with `/`, and decode the result with the forgiving-base64 decode of the WHATWG Infra Standard. A segment that holds `+` or `/` before the replacement fails. The decode ignores ASCII white space anywhere in the segment, and it accepts `=` padding that brings the length to a multiple of four. The base64url encoding of RFC 7515 allows neither.

## Compatibility

[The general rule](../README.md#versions-and-compatibility) applies. For this contract it means the following.

These parts must stay wire-compatible with the reference, because they cross between a hub and a daemon.

- The JSON form of a bundle and of an override.
- The signed bundle: the three fields of the message, the algorithm, the `typ` value, and the canonical JSON payload.
- The `decision` and the `area` of an evaluation, for the same bundle and query. Two implementations that disagree would enforce two different policies.

These parts may change, because they never leave one process.

- The shape of the evaluation query and of the evaluation result. A new version of the contract can rename their fields.
- The wording of `rule`, and the wording of every validation error.

A bundle, an area, a role, and an override accept unknown fields, and a bundle verifier keeps them in the verified bundle. A temporary grant and an evaluation query reject unknown fields.

## Generated and authored parts

The reference is at commit `7d80045a`.

| Part | Source |
|---|---|
| `bundle`, `override`, `signed-bundle` | Generated from the schemas in `packages/protocol/src/policy/types.ts` and `packages/protocol/src/ws-messages.ts` of the reference. |
| `eval-query`, `eval-result` | Authored. The reference defines both as TypeScript types in `packages/protocol/src/policy/types.ts`. |
| `jws-protected-header`, `jwks` | Authored from `apps/hub/src/identity/agent-tokens.ts` and `apps/daemon/src/policy/store.ts` of the reference. |
| Every `description` | Authored. |
| The rules on this page | Authored from `packages/protocol/src/policy/evaluate.ts` and `glob.ts`. |
| The verification steps of the signed bundle | Authored from `verifyBundleJws` in `apps/daemon/src/policy/store.ts`, and from the library jose 6.2.13 that it calls. |
| Every expected value in the fixtures | Computed by the `evaluate` function, the schemas, and the `verifyBundleJws` function of the reference. |

## Intentional differences from the reference

| Difference | Reason |
|---|---|
| `now` is required in a query. The reference reads the system clock when `now` is absent. | The evaluator is pure. The same bundle and query must always give the same result. |
| `command` is required in a query for `exec`. The reference falls back to `path`. | The reference marks the fallback as deprecated and plans to remove it. |
| An `expires_at` of an override that is not [a timestamp](#timestamps) makes the override inactive. The reference passes the string to the JavaScript date parser. That parser also reads strings outside the form, such as a date with no time, the hour `24:00:00`, a date that is not in the calendar, such as `2026-02-29`, which it reads as the next valid day, and a time with no offset, which it reads in the time zone of the machine. For a leap second the two agree, because the parser reads no time and the override is inactive. | A result must not depend on a language runtime or on the time zone of a machine. |
| A fraction of a second is cut after its third digit, however long it is. The JavaScript engine of the reference does the same for a fraction of up to nine digits. For a longer fraction it can read a different number of milliseconds. Node.js 22 reads `.0009999999` as 9 milliseconds and `.00099999999` as 99, where the rule of this contract gives 0. | A result must not depend on a language runtime. |
| A member id and a role name are looked up as exact keys. The reference looks them up as properties of a JavaScript object, so a name that every JavaScript object has, such as `constructor` or `toString`, is found although the bundle does not define it. The eleventh open question below gives the effect. | A result must not depend on the names that one language gives to its objects. |
| `bundle.schema.json` rejects a `members` or a `roles` object whose key `__proto__` has a value of the wrong type. The validator of the reference skips that key, so it accepts `{"members": {"__proto__": 5}}`, and it drops the key from the bundle that it returns. | A schema has no way to treat one key name differently, and the stricter reading is the safe one. |
| The contract has no operation that tells whether a path is generated. The reference has one, `isGenerated`. | That operation serves the server-side check, which is outside the open-source foundation. The `generated` flag stays in the bundle and never changes a result. |
| The `rule` text is not part of the contract. | It is wording for a person. |

No fixture case depends on one of these differences. Every expected value is the value that the reference returns.

## Open questions

Each item is behavior of the reference that looks unintended, or a choice that the owner has not made. Version 1 keeps the behavior of the reference until the owner decides.

1. **A line terminator in a path can take the path out of a private area.** `**` does not match a line terminator. With a private area whose pattern is `*:**/.env*`, a read of `a/.env` is denied. A read of `.env` in a directory whose name is `a`, a line feed, and `b` matches no area, so it is allowed by default. A file system allows such a name. The path leaves a system area in the same way, but a write is still denied unless an area that the role may write matches the path without `**`. The fixtures `evaluate-globs` and `evaluate-private-and-system` pin the behavior.
2. **Some classes make the reference fail, or give a result that the glob rules do not.** The reference copies the text of a class into an ECMA-262 regular expression. A range that is out of order, such as `[z-a]`, makes it throw an error. In a class whose first character is `]`, ECMA-262 reads `[]` as a class that matches nothing, and it reads the rest of the class, with its `]`, as regular expression syntax. If the rest holds none of the syntax characters `^ $ \ . * + ? ( ) [ { } |`, the glob matches no path: `src/[]x]` matches neither `src/x` nor `src/]x]`. Otherwise the reference can throw, as for `[]**]` and `[](]`, or match paths that the glob does not seem to describe: `a[]*]` matches `a]`, and `[]|x]` matches every path that ends in `x]`. A pattern that throws makes every `read` and `write` in a repository that the pattern applies to throw an error and return no result. Version 1 does not define the result of a pattern that throws, or of a class whose first character is `]` and whose rest holds a syntax character. The fixture `evaluate-globs` pins the classes that match no path, and no fixture covers the others.
3. **`git.push` compares the repository with case, and an area pattern compares it without case.** A role with `push: ["api"]` may not push to `API`, but the pattern `api:src/**` applies to `API`. The fixture `evaluate-git-push` pins the behavior.
4. **`exec` denies `$(` but allows `$` followed by a name.** `npm test $HOME` matches the grant `npm test`. The fixture `evaluate-exec` pins the behavior.
5. **An empty string in an `exec` list allows the empty command.**
6. **Comparison without case, and the size of one character, follow JavaScript.** Version 1 keeps the rules of the reference that this item gives. An implementation that compares only ASCII letters without case, or that matches code points, does not conform. For letters outside ASCII, the reference uses two different rules of JavaScript. It matches a glob with a regular expression that has the `i` flag and not the `u` flag: two characters are equal if their uppercase forms are, and a character outside ASCII never equals an ASCII letter. It compares the repository of a pattern, and a path with the part of a glob before `/**`, after `toLowerCase`. So `k/**` matches the path `K` (U+212A KELVIN SIGN), whose lowercase form is `k`, and does not match `K/x`. An implementation that compares only ASCII letters without case gives other results for a pattern with a letter outside ASCII, and a private area can be affected. With a private area whose pattern is `*:é/**`, the reference denies a read of `É/x`, and such an implementation allows it. Where the glob rules say one character, they mean one UTF-16 code unit: the character that `?` matches, the character that a class matches, and each character of a literal prefix. So neither `?` nor a class such as `[!a]` or `[!]` matches one character outside the Basic Multilingual Plane, and `[!a][!a]` does. Two literal prefixes that match the same path are one inside the other, so counting code units ranks them as counting code points does, except for a pattern that holds a lone surrogate. The fixture `evaluate-globs` pins the rules for `?` and for a class. No fixture holds a letter outside ASCII or a lone surrogate.
7. **A bundle verifier accepts a signed payload that is not canonical JSON.** A hub always signs canonical JSON, and a bundle verifier does not check that it did. The fixture `verify-signed-bundle` pins the behavior.
8. **A bundle verifier accepts a protected header with no `kid`** when exactly one key of the set is a candidate. The fixture `verify-signed-bundle` pins the behavior.
9. **`repos` has no effect on an evaluation.** A `push` grant of `*` also allows a repository that `repos` does not list.
10. **The result field is named `decision`**, which is also the word for a Room entry. The [glossary](../../docs/glossary.md) records both uses. The documentation calls the answer of the evaluator a result, and the field keeps the name that the reference uses.
11. **The reference finds a role that the bundle does not define, if the role has the name of a property of every JavaScript object.** A member that `members` maps to the role name `constructor` or `toString` has no role under the rules of this page, so a temporary grant or an override gives the member nothing. In the reference such a member counts as a member with a role that grants nothing, so a temporary grant or an override for the member applies, and the write is allowed. Here version 1 does not keep the behavior of the reference. The result is deny, as [the differences](#intentional-differences-from-the-reference) record. No fixture covers the case, because every expected value comes from the reference, and the names in question belong to the JavaScript runtime and not to the contract. The owner decides whether the reference changes.
12. **A bundle verifier reads a token and its payload leniently.** It accepts these forms, which RFC 7515 or a strict JSON parser rejects: `=` padding or ASCII white space in a segment, white space after the token, a byte order mark at the start of the header or of the payload, a repeated key in the header or in the payload, invalid UTF-8 in the payload, and an unencoded payload under RFC 7797. The signature segment is not part of the signing input, so anyone can add padding or white space to it, or a line feed after the token, and the token still verifies. Every other form needs the private key of the hub. A hub signs canonical JSON under a header with only `alg`, `typ`, and `kid`, so it makes none of these forms. The fixture `verify-signed-bundle` pins the behavior.
13. **`exec` allows U+2028 and U+2029, which ECMA-262 counts as line terminators.** Step 1 denies a line feed and a carriage return, and step 2 treats the other two as white space. `npm test` followed by U+2028 and `x` matches the grant `npm test`. The fixture `evaluate-exec` pins the behavior.
