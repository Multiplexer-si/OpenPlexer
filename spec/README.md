# spec

`spec/` holds the language-independent contracts: schemas, examples, and data-only compatibility fixtures. Another tool can produce and validate every message from these files, and test its behavior against them, without reading Go code.

## Contracts

| Directory | Contract | Version |
|---|---|---|
| [`canonical-json/`](canonical-json/README.md) | Canonical JSON, the byte form that the bundle signature and the audit hash are computed over | 1 |
| [`policy/`](policy/README.md) | The policy bundle, overrides, policy evaluation, and the signed bundle | 1 |
| [`room/`](room/README.md) | Room events, which include claims and decisions, the transport envelope, the overlap rules for claims, and the rules for order, expiry, reconnection, and errors | 1 |
| [`audit/`](audit/README.md) | The audit record, its hash, chain verification, and the JSON export | 1 |
| [`adapter/`](adapter/README.md) | How an adapter reads the hook input of a coding agent into policy queries, and answers the hook | 1 |

Each README states what the contract covers, its rules, and what is still open.

## Layout

```
spec/
	fixture-file.schema.json            the format of every fixture file
	<contract>/
		README.md                       the rules of the contract
		v1/
			<name>.schema.json          one schema
			examples/<name>/*.json      valid instances of <name>.schema.json
			fixtures/*.json             inputs with their expected results
```

## Schemas

Every schema is a JSON Schema in draft 2020-12.

The `$id` of a schema is `urn:openplexer:spec:<contract>:v1:<name>`. The `$id` is a name, not an address. To find the file of an `$id`, remove the prefix `urn:openplexer:spec:`, replace each `:` with `/`, and add `.schema.json`. A `$ref` from one schema to another uses the `$id`, so load every schema file before you compile one.

A `format` is an annotation. Where a format must hold, the schema also has a `pattern` that enforces it. A validator that fails on a format that it does not know, such as ajv with its default options, needs format validation turned off or the `date-time` format registered.

An object schema with `"additionalProperties": {}` or with no `additionalProperties` accepts unknown fields. An object schema with `"additionalProperties": false` rejects them.

### Patterns

A `pattern` is a regular expression in the ECMA-262 dialect, as JSON Schema defines. Many validators use another engine, so every pattern under `spec/` stays inside a subset. For a value made only of characters of the Basic Multilingual Plane, with no lone surrogate, a pattern in the subset gives the same verdict in each of these engines:

- ECMA-262 without a flag, and with the `u` flag, which ajv and other JavaScript validators use by default.
- RE2, which Go uses.
- The `re` module of Python, Perl, and Ruby, with the anchors replaced as the last paragraph of this section says.

The subset does not cover the `v` flag of ECMA-262. In a class, that flag needs `-`, `/`, `(`, `)`, `{`, `}`, and `|` to be escaped, and it rejects a doubled punctuation character such as `++`.

For a value with a character outside the Basic Multilingual Plane, or with a lone surrogate, the engines can differ inside the subset. ECMA-262 without a flag counts such a character as two UTF-16 code units, and ECMA-262 with the `u` flag, RE2, and Python count it as one character. A negated class, such as `^[^a]$`, a counted quantifier, such as `^[^a]{2}$`, and a class range that spans U+D800 to U+DFFF can therefore give different verdicts. The JSON reader of Go turns a lone surrogate in the value into U+FFFD, so a class range that holds U+FFFD can differ too.

A pattern in the subset is made of these parts.

- `^` as the first character of the pattern, and `$` as the last.
- A literal character: any character of the Basic Multilingual Plane except the syntax characters `^ $ \ . * + ? ( ) [ ] { } |` and U+FFFD. A character outside ASCII is in the pattern as the character itself. The JSON file writes it as a `\u` escape of JSON, which a JSON parser removes before the engine sees the pattern.
- `\` before a syntax character or `/`, such as `\.`.
- The classes `[...]` and `[^...]`. A member is a literal character, `\` before a syntax character, `/`, or `-`, or a range of two such members joined by `-`. A member can also be one of the syntax characters `^ $ * + ? ( ) { } |` with no `\`.
- The groups `(...)` and `(?:...)`, and `|`.
- The quantifiers `?`, `*`, `+`, `{n}`, `{n,}`, and `{n,m}`, after a literal character, an escape, a class, or a group. In `{n,m}`, `m` is not less than `n`. `{0}` and `{0,0}` are not in the subset. The count of `{n,m}` is `m`, and the count of `{n}` is `n`. The count of `{n,}` is `n`, or 1 if `n` is 0. A count is at most 1000, and so is the product of the counts of counted quantifiers inside each other, such as the `{10}` in `(?:a{100}){10}`. `?`, `*`, and `+` do not change the product, so `(?:(?:a{10}){0,}){125}` is outside the subset, as `(?:a{10}){125}` is.

Everything else is outside the subset, because engines disagree about it. Some examples:

- An escape of a letter or a digit, such as `\s`, `\d`, `\w`, `\b`, or `\u0020`.
- An escape of another punctuation character, such as `\-` outside a class or `\@`. ECMA-262 with the `u` flag rejects it.
- A `]`, `{`, or `}` with no `\` outside a class, unless it is part of a quantifier. ECMA-262 with the `u` flag rejects it. `{,2}` is a quantifier in Python, Perl, and Ruby, and literal text in ECMA-262 without a flag and in RE2.
- A quantifier after `^`, after another quantifier, or with nothing before it. ECMA-262 rejects `^*`, and Perl reads `a*+` as one quantifier.
- A count above 1000, or counts inside each other whose product is above 1000, such as `a{1001}` or `(?:a{100}){11}`. RE2 rejects them.
- `{n,m}` with `m` less than `n`, such as `a{3,2}`. ECMA-262 and RE2 reject it.
- `{0}` and `{0,0}`, which repeat nothing. Perl 5.34 can match a value that still ends with the atom before such a quantifier, when the quantifier is right before `$`.
- A `.` with no `\` before it, inside or outside a class.
- A group that starts with `(?`, except `(?:`.
- A `[` inside a class with no `\` before it. RE2 reads `[[:alpha:]]` as the POSIX class of letters, and ECMA-262 reads it as a class of the characters `[:alph`, followed by `]`.
- A `]` as the first member of a class, after the `^` of `[^...]` if there is one. RE2 reads `[]a]` as a class of `]` and `a`, and ECMA-262 reads it as a class that matches nothing, followed by `a]`.
- Two `-`, two `&`, two `|`, or two `~` in a row inside a class, such as `[a&&b]` or `[+--]`. Ruby reads `&&` as the intersection of two classes, and Python `re` warns about all four, because it keeps them for set operations. A validator that turns warnings into errors fails on them.
- A character outside the Basic Multilingual Plane. ECMA-262 without the `u` flag reads it as two UTF-16 code units, and RE2 reads it as one character.
- U+FFFD. Some JSON readers, such as the one in the Go standard library, turn a lone surrogate escape such as `\ud800` into U+FFFD, so the pattern that the engine sees differs from the pattern in the file.

In ECMA-262 and RE2, `^` matches only at the start of the value, and `$` matches only at its end. Other engines differ. In Python `re` and Perl, `$` also matches before a line feed at the end of the value, so there a timestamp followed by a line feed passes the timestamp pattern. In Ruby, `^` and `$` match at the start and the end of every line, so a value passes if any line in it matches. A validator on one of these engines replaces the anchors: the final `$` with `\Z` in Python and with `\z` in Perl and Ruby, and in Ruby the first `^` with `\A`. Two cases of the policy fixture `validate-bundle` fail on a validator that does not. `a timed grant whose expires_at ends in a line feed is invalid` fails in Python, Perl, and Ruby. `a timed grant whose expires_at has a line before the timestamp is invalid` fails in Ruby only.

### Numbers

A reader holds every number as an IEEE 754 double, as [canonical JSON](canonical-json/README.md#the-input) does. `1.0000000000000001` is therefore the integer 1, and it is valid where a schema asks for an integer.

A number outside the range of a double, such as `1e400`, is read as infinity, as canonical JSON also reads it. Infinity is not a finite number, so the value is invalid where a schema asks for a number or an integer. It is valid where a schema accepts any value, such as the `payload` of an envelope whose `type` is not `event`, as in the reference. Some JSON readers, such as the one in the Go standard library, fail on such a number. A validator that uses one reads the number as infinity instead.

## White space

Where a contract says white space, it means these 25 code points and no others.

| Code points | Names |
|---|---|
| U+0009 to U+000D | Tab, line feed, line tabulation, form feed, and carriage return |
| U+0020 | Space |
| U+00A0 | No-break space |
| U+1680 | Ogham space mark |
| U+2000 to U+200A | The eleven spaces from en quad to hair space |
| U+2028, U+2029 | Line separator and paragraph separator |
| U+202F | Narrow no-break space |
| U+205F | Medium mathematical space |
| U+3000 | Ideographic space |
| U+FEFF | Zero width no-break space |

The set is the white space and the line terminators of ECMA-262, which the reference uses. Other definitions differ. U+0085, U+180E, and U+200B are not white space here.

Two rules use the set: the `exec` rule of the [policy contract](policy/README.md#exec), and the rule for the text of a question and of an answer in the [Room contract](room/README.md#events).

## Examples

Every file under `v1/examples/<name>/` is a valid instance of `v1/<name>.schema.json`. Every schema in a version directory has at least one example.

## Fixture files

A fixture file is a JSON document that lists cases for one operation of one contract. [`fixture-file.schema.json`](fixture-file.schema.json) defines the format. Its `$id` is `urn:openplexer:spec:fixture-file`.

| Field | Meaning |
|---|---|
| `contract` | The contract directory that the file belongs to. |
| `version` | The version of the contract. |
| `operation` | What an implementation does with each case. The table below lists the operations. |
| `description` | What the file covers. |
| `reference` | The commit of the reference that produced the expected values, and the source files that the cases exercise. |
| `schema` | For `validate` only: the `$id` of the schema under test. |
| `shared` | Input that every case of the file shares. |
| `cases` | The cases. |

Each case has these fields.

| Field | Meaning |
|---|---|
| `name` | A name that is unique in the file. |
| `why` | The behavior that the case pins. |
| `input` | What the implementation receives. |
| `expected` | What the implementation must return. |
| `informational` | Output of the reference that is not part of the contract, such as the wording of a message. Do not compare it. |

To run a case, do the operation on `shared` and `input`, and compare the whole result with `expected`.

| Operation | Contract | `shared` | `input` | `expected` |
|---|---|---|---|---|
| `evaluate` | policy | `bundle` | An evaluation query | `decision`, and `area` if an area decided. Or `throws`, see below |
| `verify-signed-bundle` | policy | `jwks` | `signature`, a compact JWS | `ok`, and `bundle` if `ok` is true |
| `validate` | policy, room | None | Any JSON value | `valid` |
| `patterns-overlap` | room | None | Two patterns, `a` and `b` | `overlap`, or `throws` |
| `path-sets-overlap` | room | None | Two lists of patterns, `a` and `b` | `overlap`, or `throws` |
| `deliver` | room | None | `since`, and `steps`: stored events that the hub replays, new events, and the end of the replay | `sent`, the `seq` of each event that the hub sends, and `last_seq` |
| `active-claims` | room | None | `events`, in the order that a receiver accepts them, and `now`, the time at which it accepts them and lists the claims | `claims`, the active claims in the order of their events |
| `close-code` | room | None | `code`, the code of an error | `close_code`, the close code that the hub sends after the error, or null if it keeps the connection |
| `canonicalize` | canonical-json | None | `json`, a JSON text | `canonical`, and `sha256` of its UTF-8 bytes |
| `verify-chain` | audit | None | `records`, in chain order | A chain verification result |
| `bash-targets` | adapter | None | `command`, a shell command line | `targets`, the paths that the line writes |
| `exec-segments` | adapter | None | `command`, a shell command line | `segments`, the words of each command |
| `patch-targets` | adapter | None | `patch`, the text of an `apply_patch` call | `targets`, the paths of the patch, or null for a patch that Codex rejects |
| `pre-tool` | adapter | `agent`, `bundle`, `repo`, `now` | `member` and `hook`, the JSON that the agent writes to the hook | `decision`, `allow` or `deny` |

For `validate`, validate `input` against the schema that `schema` names, and compare the verdict with `expected.valid`.

For `evaluate`, an `expected` of `{"throws": true}` means that the reference throws an error and returns no result. The contract does not define a result for such a case, but an implementation must fail closed on it. So a runner in any language passes the case if the implementation returns `deny` with no `area`, or if it fails with an error and returns no result. The runner fails the case if the implementation returns `allow`, or a result with an `area`. `informational.error` holds the message of the reference. The [second open question of the policy contract](policy/README.md#open-questions) lists the inputs, and the fixture `evaluate-class-syntax` holds the cases.

The fixture-file schema refers to the contract schemas. A fixture file that validates against it therefore has a valid `shared`, a valid `input`, and a valid `expected` in every case. `validate` is the exception, because its inputs are invalid on purpose.

For `patterns-overlap` and `path-sets-overlap`, an `expected` of `{"throws": true}` means that the reference throws an error and returns no result. An implementation must fail closed on such a case: a runner passes the case only if the implementation reports an overlap. `informational.error` holds the message of the reference. The [seventh open question of the room contract](room/README.md#open-questions) lists the inputs.

## Versions and compatibility

The version of a contract is the name of its version directory. Every contract is at version 1.

### Writers and receivers

A writer produces a message, and a receiver reads it. Each side has one duty about fields that the contract does not define.

- A writer sends only the fields that the contract defines. A writer that passes on a message that it received also sends the unknown fields of that message, unchanged.
- A receiver accepts an unknown field wherever the schema accepts unknown fields. It does not fail because of that field, and it keeps the field when it stores or forwards the message.

A message from a writer that does its duty is a conforming message. The compatibility rule protects conforming messages.

### Changes inside version 1

A change inside version 1 may do only these things.

- Add an optional field to an object that accepts unknown fields. No conforming writer sent a field with that name before, so no conforming message changes its meaning. A receiver that was built before the change keeps the new field as an unknown field.
- Add an example.
- Add a fixture case that pins behavior that version 1 already has.
- Correct a description or a README.

A new optional field can change the verdict of a schema on a message that is not conforming. Today `{"summary": "x", "files2": 5}` is a valid `status` body, because `files2` is an unknown field. If version 1 later defines `files2` as a string, that body becomes invalid. No conforming writer sends `files2` today, so the change breaks no conforming message.

### Changes that need a new version

Any other change needs a new version, in a new directory `v2/` next to `v1/`. These changes need a new version.

- Remove or rename a field, or change its type or its meaning.
- Make an optional field required.
- Reject a message that a conforming version 1 writer can send.
- Accept a value of a defined field that version 1 rejects, such as a new value in a closed list. A version 1 receiver rejects a message with that value.
- Change one byte of the canonical JSON output, of the input to the audit hash, or of the payload that a bundle signature covers.
- Change the expected result of a fixture case.

Each contract README says what else the rule means for that contract.

Version 1 is not released yet. Until the first release that implements it, the open questions in each contract README can still change version 1.

## What comes from the reference

The existing TypeScript implementation is the reference. Every file under `spec/` except the README pages is written by the extractor in [`tools/extract/`](../tools/extract), which runs the reference at commit `7d80045a`.

- A generated schema is the output of the JSON Schema export of Zod, the validation library that the reference uses. The descriptions in it are authored. The extractor writes `[0-9]` where Zod writes `\d` in a pattern, and `0-9` where Zod writes `\d` inside a class, to keep the pattern inside [the portable subset](#patterns).
- An authored schema covers a shape that the reference defines only as a TypeScript type or in code. Each contract README lists the authored parts.
- An example is authored. The extractor fails if the reference rejects one.
- A fixture input is authored. Every expected value is computed by the reference code.

[Regenerate the contracts and fixtures](../CONTRIBUTING.md#regenerate-the-contracts-and-fixtures) gives the commands.

## What checks these files

The Go tests in `internal/spectest` run in `make check`. They need neither the reference nor the Go implementation.

- Every file under `spec/` is in a place that [the layout](#layout) names.
- Every schema compiles, and its `$id` matches its path.
- Every pattern, and every key of a `patternProperties` object, stays inside [the portable subset](#patterns).
- Every example validates against its schema, and every schema in a version directory has an example.
- A number outside the range of a double gets the verdict that [Numbers](#numbers) gives.
- Every audit record among the examples has the hash that [the hash rule](audit/README.md#the-hash-of-a-record) gives, and the export example verifies.
- Every fixture file validates against the fixture-file schema.
- For every `validate` case, the schema under test belongs to the contract and the version of the fixture file, and the verdict of the published schema equals the verdict of the reference.
- For every `canonicalize` case, `sha256` is the SHA-256 of `canonical`.
- For every `verify-chain` case, the verification steps of the audit contract give the expected result.

The tests read numbers as doubles, and a number outside their range as infinity, as [Numbers](#numbers) says. The validator in the tests compiles each pattern with the Go engine. Inside [the portable subset](#patterns), the Go engine and an ECMA-262 engine give the same verdict on a value made only of characters of the Basic Multilingual Plane, with no lone surrogate.

The Go implementation runs the fixtures against its own code. The tests in `internal/policy` run every `validate` and every `evaluate` case of the policy contract. The tests in `internal/room` run every case of the Room contract. No Go code runs the `verify-signed-bundle` cases yet.
