# spec/room

This contract defines the stored Room event, the bodies of twelve event types, the transport envelope, and the rules that decide whether two claims overlap. The contract is at version 1.

A claim is the body of a `claim` event, and a `release` event ends it. A decision is the body of a `decision` event, and a `decision.confirm` event records that a person confirmed or rejected it.

The field descriptions are in the schemas. This page gives the rules that a schema cannot express.

## Files

| File | Content |
|---|---|
| `v1/room-event.schema.json` | A stored Room event. `$defs` holds one body schema for each event type. |
| `v1/envelope.schema.json` | The object that wraps every WebSocket message between a daemon and the hub. |
| `v1/examples/room-event/` | One valid event for each event type, and one event from a sub-agent. |
| `v1/examples/envelope/` | An envelope that carries an event, and an envelope about the connection. |
| `v1/fixtures/validate-room-event.json` | Valid and invalid events: at least one of each for every event type. |
| `v1/fixtures/validate-envelope.json` | Valid and invalid envelopes. |
| `v1/fixtures/claims-patterns-overlap.json`, `v1/fixtures/claims-path-sets-overlap.json` | Pairs of patterns and pairs of path lists, with whether they overlap. |

## Events

An event has the fields `id`, `seq`, `mission`, `type`, `agent`, `on_behalf_of`, `at`, and `body`. The `type` selects the schema of `body`.

| `type` | Body |
|---|---|
| `status` | What an agent is doing. |
| `claim` | A claim on a piece of work, with the paths that it covers and the seconds until it expires. |
| `release` | The end of a claim. |
| `decision` | Something that the team has settled. |
| `decision.confirm` | A person confirmed or rejected a decision. |
| `question` | A question from an agent. |
| `answer` | An answer to a question. |
| `blocked` | An agent cannot continue. |
| `human` | A message that a person wrote. |
| `policy.denied` | The gate denied an action. |
| `policy.override` | A lead granted an override. |
| `policy.override.revoked` | A lead revoked an override. |

Every body also accepts `thread_id` and `reply_to`.

`agent` names who posted the event. For an event that an agent session posted, `agent.id` is the id of the session, `agent.vendor` is the coding agent, and `on_behalf_of` is the member that the agent acts for. An event that a person caused has no agent session: `agent.id` and `on_behalf_of` are the id of the member, and `agent.vendor` is `human`. The hub of the reference never lets an agent session post `human`, `decision.confirm`, `policy.override`, or `policy.override.revoked`, so these four types always have the form of an event that a person caused. The reference builds `agent` in `apps/hub/src/ws/fanout.ts`, and rejects those types from an agent session in `apps/hub/src/room/moderation.ts`, at commit `7d80045a`.

An event with any other `type` is invalid, whatever its body.

`text` and `addressed_to` of a `question`, and `text` of an `answer`, need one character that is not [white space](../README.md#white-space).

## The envelope

Every message on the WebSocket connection is one envelope. `v` is the version of the wire protocol, and this contract describes version 1. `type` names the message, and `payload` holds its content. The hub sets `ts` on every message that it sends. It sets `room`, `seq`, and `event_id` on a message that carries an event. A message about the connection has `room` when it concerns one Room, and the answer to an accepted post also has the `seq` and the `event_id` of the new event. On a message that carries an event, `ts` is the `at` of the event, the time that the hub stored it, `room` is the `mission` of the event, and `sender` is the poster of the event, not the hub. The hub of the reference uses the id of a mission as the id of its Room. The reference builds that envelope in `apps/hub/src/ws/fanout.ts`, and every other message of the hub in `apps/hub/src/ws/server.ts`, at commit `7d80045a`.

A stored event travels as the `payload` of an envelope with the `type` `event`. The envelope schema validates the payload of such a message as a stored event. Version 1 defines no other payload, so the schema accepts any `payload` with any other `type`.

### An event that a receiver cannot validate

A receiver can get an `event` message whose payload is not a valid event of version 1. The usual cause is an event type that the receiver does not know. The hub of the reference sends nine event types that version 1 does not define, in the same envelope.

The receiver does these things.

1. It skips the event. It does not store the event, and it does not show the event to an agent.
2. It keeps the connection open, and it sends no error.
3. It counts the event as received. If the envelope has `room` and `seq`, the receiver records that `seq` as the highest `seq` that it has received for that Room, unless it already has a higher one. After a reconnection it resumes after that `seq`, so the hub does not send the skipped event again.

A validator still reports such an event as invalid. The rule is about what a receiver does on a connection.

The daemon of the reference also skips the message and keeps the connection, and it sends no error. It differs in the third step, because it does not record the `seq` of a message that it skips. Its highest `seq` moves on with the next event that it accepts, because it does not look for gaps. If it reconnects before that, it asks for the skipped event again, and it skips the event again. The hub of the reference accepts a resume after any `seq` that it still stores, so a version 1 receiver and the daemon of the reference both work with it.

The hub of the reference receives a new event in a message of the type `event.post`, which version 1 does not define. It answers a post of an unknown event type with an `error` message. It stores nothing, assigns no `seq`, and keeps the connection.

## Claim overlap

A claim lists paths and globs in `paths`. Two claims overlap if one entry of the first claim overlaps one entry of the second. A claim with no entries overlaps nothing.

Before two entries are compared, each one is normalized: replace every `\` with `/`, remove every leading `./`, and replace each run of `/` with one `/`. A `..` segment is not resolved. An entry that contains `*`, `?`, or `[` is a glob. Every other entry is a path.

| Entries | They overlap if |
|---|---|
| Two paths | The two are equal. |
| A glob and a path | The glob matches the path. |
| Two globs | One literal prefix is a prefix of the other. The literal prefix of a glob is the text before its first `*`, `?`, or `[`. |

The reference also tests whether one glob matches the literal prefix of the other. That test never changes the result, because a glob matches only text that starts with its own literal prefix.

The rule for two globs reports more overlaps than exist. Two globs with the same literal prefix overlap even if no path matches both. A claim is advice to other agents and not a lock, so a false overlap costs little.

The glob rules are the rules of [the policy glob dialect](../policy/README.md#the-policy-glob-dialect), with two differences.

- The claims dialect compares letters with case. `src/Cart.ts` and `src/cart.ts` do not overlap.
- `dir/**` does not match `dir` itself.

The rule for line terminators is the same in both dialects. `**` matches no line feed, carriage return, U+2028, or U+2029, and every other part of a glob matches them. A claim on `src/**` therefore does not overlap a path under `src/` that has a line feed in it, and a claim on `src/*` does.

One character is one UTF-16 code unit in both dialects, as the sixth open question of the [policy contract](../policy/README.md#open-questions) says. `?` and a class match one code unit, so a claim on `t[!a]x` does not overlap the path of `t`, U+1F600, and `x`.

## Compatibility

[The general rule](../README.md#versions-and-compatibility) applies. For this contract it means the following.

Every part of this contract crosses between a daemon and the hub, so every part must stay wire-compatible with the reference: the field names and types of an event and of an envelope, the names of the event types, and the value of `v`. Only the wording of a validation error may change.

An event, a body, and an envelope accept unknown fields, and a receiver keeps them. Three objects inside them also accept unknown fields: `agent` of an event, `sender` of an envelope, and `needs` of a `blocked` body.

A receiver [skips an event whose `type` it does not know](#an-event-that-a-receiver-cannot-validate). A new event type therefore does not stop a receiver that was built before it, but that receiver does not see the new events. The list of event types is closed, so a new event type is a new value in a closed list, and the general rule asks for a new version. The first open question below asks whether that rule should stay.

## Generated and authored parts

The reference is at commit `7d80045a`.

| Part | Source |
|---|---|
| The base object of an event and each body under `$defs` | Generated from the schemas in `packages/protocol/src/events.ts`. |
| The list of event types in `room-event` | Authored. It is the subset of the reference list that is in scope. |
| The `allOf` in `room-event` that ties `body` to `type` | Authored. The reference does the same check in code, with one lookup by `type`. |
| The `pattern` on `text` and `addressed_to` of `question`, and on `text` of `answer` | Authored. The reference trims these fields before it checks their length, which a JSON Schema cannot do. The pattern requires one character that is not [white space](../README.md#white-space). The extractor fails if the class in the pattern differs from the white space of the JavaScript runtime. |
| `envelope` | Generated from `packages/protocol/src/envelope.ts`. |
| The `if` and `then` in `envelope` that tie `payload` to the `type` `event` | Authored. The reference does the same check in `packages/protocol/src/ws-messages.ts`, in the schema that its hub and its daemon parse every message with. |
| Every `description` | Authored. |
| The overlap rules on this page | Authored from `apps/hub/src/claims/overlap.ts`. |
| Every expected value in the fixtures | Computed by the schemas and by the `patternsOverlap` and `pathSetsOverlap` functions of the reference. In `validate-envelope`, the verdict of a message with the `type` `event` comes from the message schema in `ws-messages.ts`, and the verdict of every other message comes from the envelope schema in `envelope.ts`. |

## Intentional differences from the reference

| Difference | Reason |
|---|---|
| Version 1 defines twelve event types. The reference defines nine more: `request`, `artifact`, `ledger.adopted`, `check.timeout-pass`, `handoff.offered`, `handoff.accepted`, `handoff.declined`, `handoff.completed`, and `handoff.cancelled`. A version 1 validator rejects them. | The owner has not decided whether these event types are part of the open-source foundation. |
| Version 1 defines one envelope payload, the stored event. The reference defines a payload for each of its message types, and its hub and its daemon reject a message whose `type` is not one of them. A version 1 validator accepts such a message. | The other messages depend on the transport between the daemon and the hub, which Phase 2 settles. |
| A receiver that skips an event records the `seq` of that event. The daemon of the reference does not. | A receiver that has recorded the `seq` does not ask for the same event after every reconnection. Both behaviors work with the hub of the reference. |
| A receiver keeps an unknown field of `agent`, of `sender`, and of `needs`. The reference drops an unknown field of these three objects when it parses a message, and it keeps an unknown field of an event, of a body, and of an envelope. | [The general rule](../README.md#writers-and-receivers) has no exception. No conforming writer sends such a field today, so the two behaviors differ only after version 1 adds a field to one of the three objects. |

No fixture case depends on one of these differences. Every expected value is the value that the reference returns. One case needs a note. In `validate-envelope`, the case `a type that no message defines is valid` records the verdict of the envelope schema of the reference. The message schema of the reference, which its hub and its daemon parse with, rejects that message.

## Open questions

Each item is behavior of the reference that looks unintended, or a choice that the owner has not made. Version 1 keeps the behavior of the reference until the owner decides.

1. **May version 1 gain an event type?** A receiver skips an event of an unknown type, so a new type does not stop an older receiver. The older receiver does not see the new events, and an older validator reports them as invalid. Under the general rule a new value in a closed list needs a new version, so each of the nine event types above needs a new version of the contract if it is added after the first release. The owner decides whether the list of event types is an exception.
2. **`at` is any string that is not empty.** The reference does not check that it is a timestamp.
3. **A claim with an empty `paths` list is valid.** It overlaps nothing, so it protects nothing.
4. **`action` in a `policy.denied` body, and `actions` and `scope` in a `policy.override` body, are open strings.** The policy contract has closed lists for the same values.
5. **The envelope schema accepts any `v` greater than zero and any `type` that is not empty. The reference is stricter.** Its hub and its daemon parse every message with a schema that lists their message types. A message with another `type`, or with a payload that does not fit its `type`, fails. The hub answers such a message with an `error` message, the daemon drops it, and both keep the connection. The hub closes a connection that sends a `v` other than 1. Version 1 defines the payload of `event` only. What a receiver does with any other `type`, and with another `v`, is settled with the transport in Phase 2.
6. **The reference removes the white space around `text` and `addressed_to` when it parses a `question` or an `answer`.** Version 1 says only that the value needs one character that is not white space. It does not say whether a receiver stores the trimmed value.
7. **Some classes make the overlap check of the reference fail, or give a result that the glob rules do not.** The causes are those of the second open question of the [policy contract](../policy/README.md#open-questions): a range that is out of order, such as `[z-a]`, makes the check throw an error, and a class whose first character is `]` is read as regular expression syntax. Version 1 does not define the result of a glob that throws, or of a class whose first character is `]` and whose rest holds a syntax character. The fixture `claims-patterns-overlap` pins the classes that match no path, and no fixture covers the others.
8. **`dir/**` matches `dir` in the policy dialect and not in the claims dialect.** A claim on `src/**` does not overlap a claim on the path `src`.
