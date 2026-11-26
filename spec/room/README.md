# spec/room

This contract defines the stored Room event, the bodies of twelve event types, the transport envelope, the payloads of the `resume` and `error` messages, the rules that decide whether two claims overlap, and the rules for order, expiry, reconnection, and errors. The contract is at version 1.

A claim is the body of a `claim` event, and a `release` event ends it. A decision is the body of a `decision` event, and a `decision.confirm` event records that a person confirmed or rejected it.

The field descriptions are in the schemas. This page gives the rules that a schema cannot express.

## Files

| File | Content |
|---|---|
| `v1/room-event.schema.json` | A stored Room event. `$defs` holds one body schema for each event type. |
| `v1/envelope.schema.json` | The object that wraps each WebSocket or HTTP stream message between a daemon and the hub. |
| `v1/resume.schema.json` | The payload of a `resume` message. |
| `v1/error.schema.json` | The payload of an `error` message. |
| `v1/examples/room-event/` | One valid event for each event type, and one event from a sub-agent. |
| `v1/examples/envelope/` | An envelope that carries an event, and an envelope about the connection. |
| `v1/fixtures/validate-room-event.json` | Valid and invalid events: at least one of each for every event type. |
| `v1/examples/resume/`, `v1/examples/error/` | Valid payloads of the two messages. |
| `v1/fixtures/validate-envelope.json` | Valid and invalid envelopes. |
| `v1/fixtures/validate-resume.json`, `v1/fixtures/validate-error.json` | Valid and invalid payloads of the two messages. |
| `v1/fixtures/error-close-codes.json` | The close code that follows each error code of the hub. |
| `v1/fixtures/deliver.json` | What one subscription of the hub sends, for a replay and new events in several orders. |
| `v1/fixtures/active-claims.json` | The claims that a receiver holds as active after a list of events. |
| `v1/fixtures/claims-patterns-overlap.json`, `v1/fixtures/claims-path-sets-overlap.json` | Pairs of patterns and pairs of path lists, with whether they overlap. |
| `v1/fixtures/claims-class-syntax.json` | Pairs of patterns in which a class starts with `]` or holds a range that is out of order, with whether they overlap or whether the reference throws. |

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

A stored event travels as the `payload` of an envelope with the `type` `event`. The envelope schema validates the payload of such a message as a stored event, and it accepts any `payload` with any other `type`. Version 1 defines two more payloads, each in its own schema: `v1/resume.schema.json` for the `type` `resume`, and `v1/error.schema.json` for the `type` `error`. A receiver validates the payload of those two messages against its schema after it validates the envelope.

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
| Two globs | One literal prefix is a prefix of the other, or one glob matches the literal prefix of the other. The literal prefix of a glob is the text before its first `*`, `?`, or `[`. |

The second test for two globs changes the result only for a glob that holds a class whose first character is `]`. Every other glob matches only text that starts with its own literal prefix, so the first test holds for it whenever the second does. `ab[]|c]` and `zc]*` overlap by the second test alone. Neither literal prefix, `ab` and `zc]`, starts the other, and the first glob matches `zc]`, as the [seventh open question](#open-questions) explains.

If the reference throws an error when it compares two entries, the two entries overlap. A claim is a warning, so the comparison fails closed.

The rule for two globs reports more overlaps than exist. Two globs with the same literal prefix overlap even if no path matches both. A claim is advice to other agents and not a lock, so a false overlap costs little.

The glob rules are the rules of [the policy glob dialect](../policy/README.md#the-policy-glob-dialect), with two differences.

- The claims dialect compares letters with case. `src/Cart.ts` and `src/cart.ts` do not overlap.
- `dir/**` does not match `dir` itself.

A class whose first character is `]`, and a range that is out of order, follow the seventh open question.

The rule for line terminators is the same in both dialects. `**` matches no line feed, carriage return, U+2028, or U+2029, and every other part of a glob matches them. A claim on `src/**` therefore does not overlap a path under `src/` that has a line feed in it, and a claim on `src/*` does.

One character is one UTF-16 code unit in both dialects, as the sixth open question of the [policy contract](../policy/README.md#open-questions) says. `?` and a class match one code unit, so a claim on `t[!a]x` does not overlap the path of `t`, U+1F600, and `x`.

## Order, expiry, reconnection, and errors

The four sections below give the rules that a hub and a receiver follow on a connection. A receiver is a daemon or a dashboard. Each rule has a source.

- **Reference** means that the rule is the behavior of the reference at commit `7d80045a`, and the last column names the file.
- **Chosen** means that version 1 sets the rule. The reference differs or has no rule, and [Intentional differences from the reference](#intentional-differences-from-the-reference) gives the reason.

The Go package `internal/room` models connection behavior with `Subscription` for the hub, `Receiver` for the other side, and one table for the error codes. Where a fixture pins a rule, the last column names the fixture. The tests of `internal/room` pin every other rule that a `Subscription` or a `Receiver` follows. The hub implements and tests the storage and claim rules O1, E5, and E8 under [issue 19](https://github.com/Multiplexer-si/OpenPlexer/issues/19).

### Order

| # | Rule | Source | Evidence |
|---|---|---|---|
| O1 | The hub gives each event of a Room a `seq` that is one more than the `seq` of the event before it. The first event has the `seq` 1. | Reference | `apps/hub/src/room/events.ts` |
| O2 | On one connection, the hub sends the events of a Room in rising `seq`. It does not send an event whose `seq` is at or below the last one that it sent on that connection, so an event that the hub reads twice is sent once. | Reference | `apps/hub/src/ws/subscriptions.ts`, fixture `deliver` |
| O3 | While the hub replays stored events to a connection, it holds each new event of the Room. It sends the held events after the replay, by rule O2. | Reference | `apps/hub/src/ws/subscriptions.ts`, fixture `deliver` |
| O4 | A receiver keeps the highest `seq` that it has received for each Room. It accepts an event whose `seq` is higher, and it drops an event whose `seq` is equal or lower. | Reference | `apps/daemon/src/sync/client.ts` |
| O5 | A gap in `seq` is not an error. A receiver does not wait for a missing `seq`, and it does not ask for one. The hub can hold back an event from one kind of receiver. | Reference | `apps/daemon/src/sync/client.ts`, `apps/hub/src/ws/subscriptions.ts` |
| O6 | The Room of an `event` message is the `room` of the envelope. If the envelope has no `room`, it is the `mission` of the event. The `seq` that counts is the `seq` of the event. | Reference | `apps/daemon/src/sync/client.ts` |
| O7 | A receiver counts an event that it skips, as [An event that a receiver cannot validate](#an-event-that-a-receiver-cannot-validate) says. | Chosen | |
| O8 | A receiver skips a message whose `v` is not 1. It does not count the message, and it keeps the connection. | Chosen | |

### Claims and expiry

A receiver builds the claims of a Room from the `claim` and `release` events that it accepts by rule O4, in the order of their `seq`.

| # | Rule | Source | Evidence |
|---|---|---|---|
| E1 | The id of a claim is the `claim_id` of the body of its `claim` event, when that is a string. Otherwise it is the `id` of the event. The body schema does not name `claim_id`. The hub of the reference sets it. | Reference | `apps/daemon/src/sync/cache.ts`, fixture `active-claims` |
| E2 | A claim expires `ttl_seconds` after the `at` of its event. `at` is read to the millisecond. | Reference | fixture `active-claims` |
| E3 | A claim is active while its expiry is later than the current time. At the instant of its expiry it is not active. The hub and the receiver use the same comparison. | Reference | `apps/hub/src/claims/service.ts`, fixture `active-claims` |
| E4 | A receiver stops listing a claim when it expires. It needs no event for that. | Reference | fixture `active-claims` |
| E5 | The hub ends an expired claim at its next claim operation. For each one it posts a `release` event with the `outcome` `expired`, in the name of the agent session that held the claim. No timer runs. | Reference | `apps/hub/src/claims/service.ts` |
| E6 | A `claim` event with the id of a held claim replaces that claim. | Reference | fixture `active-claims` |
| E7 | A `release` event ends the claim that its `claim_id` names, if `agent.id` of the release is `agent.id` of the claim event. A release from another agent session changes nothing. | Reference | fixture `active-claims` |
| E8 | The hub sends no event when an agent renews a claim. Every other receiver therefore stops listing the claim at its first expiry. | Reference | `apps/hub/src/claims/service.ts` |
| E9 | If a receiver cannot read the `at` of a `claim` event as a timestamp, the claim expires `ttl_seconds` after the time at which the receiver got the event. | Reference | `apps/daemon/src/sync/cache.ts`, fixture `active-claims` |

### Reconnection

| # | Rule | Source | Evidence |
|---|---|---|---|
| C1 | A receiver keeps the highest `seq` of each Room across connections and across restarts. | Reference | `apps/daemon/src/sync/state.ts` |
| C2 | After a connection opens, the receiver sends one `resume` message for each Room that it follows. `room` of the envelope names the Room, and `last_seq` is the highest `seq` that the receiver has, or 0. | Reference | `apps/daemon/src/sync/client.ts` |
| C3 | The hub answers a `resume` with every stored event of the Room whose `seq` is higher than `last_seq`, in order, and then with new events, by rules O2 and O3. A receiver that follows rule O4 therefore loses no event and gets no event twice. | Reference | `apps/hub/src/ws/fanout.ts`, fixture `deliver` |
| C4 | The hub accepts a `last_seq` that is higher than the `seq` of the last event of the Room. It then sends nothing until an event passes `last_seq`. A receiver never sends a `last_seq` that it did not receive. | Reference | fixture `deliver` |
| C5 | A second `resume` for a Room on one connection replaces the first. | Reference | `apps/hub/src/ws/server.ts` |
| C6 | An `error` with the code `behind` and a `room` means that the hub has stopped sending the events of that Room on this connection. The receiver sends a `resume` for the Room at once, even if it already sent one. | Reference | `apps/hub/src/ws/server.ts`, `apps/daemon/src/sync/client.ts` |

#### HTTP GET binding

Each GET stream follows one Room. The request has no body. The hub authenticates the request before sending any frames. Successful responses use status `200`, `Content-Type: application/x-ndjson`, and `Cache-Control: no-store`. Each nonempty line holds one v1 envelope. A blank line is a heartbeat.

| Rule | HTTP binding |
| --- | --- |
| C2 | Open `GET /api/v1/rooms/{room}/stream?last_seq=N`. Use the highest saved sequence for that Room, or 0. The replay route's cursor rules apply. |
| C5 | Close the response and open a new GET to replace the subscription. |
| C6 | A `behind` envelope ends delivery on that response. Close it and open a new GET at once from the highest saved sequence. |

When a stream reaches its budget or deadline, the hub ends the response with clean EOF. This can be an empty `200` if the deadline arrives before the first frame.

If a page read fails before the response starts, the hub returns an HTTP error status with a JSON error body. If the response has started, the hub aborts the body so the receiver gets a read error. If an in-stream page read detects revocation while the response is writable and before any stream deadline, the hub sends an `unauthorized` envelope and closes the stream. The stream can end with clean EOF before a page read detects revocation. On the next GET with the revoked token, the Go hub returns HTTP `401`. The Worker returns HTTP `401` if its first authenticated page read detects the revocation before that GET's deadline. If the deadline arrives first, the Worker returns an empty HTTP `200`.

The receiver saves the cursor after delivering an event or counting a skip. After the stream ends, it opens a new GET with that cursor. It reports HTTP and stream errors and reconnects quietly after clean EOF. It drops sequences at or below the cursor under O4. If the receiver process stops between delivery and saving the cursor, it may deliver that event again.

The Go hub also serves the full-duplex POST stream. It follows one Room and accepts the v1 `resume` envelope. A new `resume` replaces the subscription on that stream. [Hub HTTP API](../../docs/hub.md#http-api)

How long a receiver waits before it opens a new connection is not part of the contract. The message `snapshot_required` of the reference belongs to retention, which is out of scope.

### Errors

The hub reports an error in a message with the `type` `error`. Its payload has `code`, `message`, and an optional `client_event_id`, as `v1/error.schema.json` defines. The envelope has `room` when the error concerns one Room. `code` is the contract. The wording of `message` is not.

| # | Rule | Source | Evidence |
|---|---|---|---|
| X1 | After an error with the code `unsupported_version`, the hub closes the connection with the close code 4400. After `unauthorized` or `token_expired`, it closes with 4401. After `mpx_outdated`, it closes with 4426. After every other code that it sends, it keeps the connection. | Reference | `apps/hub/src/ws/server.ts`, fixture `error-close-codes` |
| X2 | A receiver sends no `error` message. It skips a message that it cannot read, and it keeps the connection. | Reference | `apps/daemon/src/sync/client.ts` |
| X3 | An error that answers a post has the `client_event_id` of the post. The poster sends the post again after `rate_limit` or `internal`. It treats `coalesced` as success: the hub stored nothing new, and the post needs nothing more. Every other code refuses the post for good. | Reference | `apps/daemon/src/sync/client.ts` |
| X4 | A receiver never closes the connection because of a code. Only the hub closes it, by rule X1. A receiver treats a code that this table does not list as the last row: the post is refused for good. | Reference | `apps/daemon/src/sync/client.ts` |
| X5 | `right_to_post` refuses a post for good, whatever its `message`. | Chosen | |

| `code` | Meaning | After it the hub | A post that it answers |
|---|---|---|---|
| `unsupported_version` | The `v` of a message is not 1, or the client offers no version that the hub has. | closes with 4400 | is refused |
| `unauthorized` | The client sent a message before it proved who it is, or its proof is not valid. | closes with 4401 | is refused |
| `token_expired` | The token of the agent session expired. | closes with 4401 | is refused |
| `mpx_outdated` | The program of the client is older than the oldest version that the hub accepts. | closes with 4426 | is refused |
| `already_authenticated` | The client proved who it is a second time. | keeps the connection | is refused |
| `bad_message` | The hub cannot read the message. | keeps the connection | is refused |
| `schema` | A post does not fit the schema of its event type. | keeps the connection | is refused |
| `right_to_post` | The sender may not post this event type. | keeps the connection | is refused |
| `forbidden` | The sender may not do this in this Room. | keeps the connection | is refused |
| `unsupported` | The hub does not take this message type from a client. | keeps the connection | is refused |
| `rate_limit` | The sender posts too often. | keeps the connection | is sent again |
| `internal` | The hub failed. | keeps the connection | is sent again |
| `coalesced` | The hub merged the post into an earlier one. | keeps the connection | needs nothing more |
| `behind` | The hub stopped sending the events of the Room, by rule C6. | keeps the connection | is refused |
| any other code | | keeps the connection | is refused |

The messages that carry a post and prove who a client is belong to the hub, which [issue 19](https://github.com/Multiplexer-si/OpenPlexer/issues/19) builds. Version 1 lists their codes here so that a receiver built on this contract reacts to each one.

The fixture `error-close-codes` lists every code that the socket server of the reference names in its source, with the close code that follows it. It holds eight codes that the table does not list: `bind_failed`, `claim_failed`, `claim_forbidden`, `claim_not_found`, `claim_repo_required`, `code`, `secret`, and `size`. Each one belongs to a message that version 1 does not define, and the hub keeps the connection after each one, as the last row says. The hub of the reference can also send a code that it takes from another service at run time, which the fixture cannot list. It sends such a code with a function that keeps the connection.

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
| `resume` and `error` | Generated from the payload schemas of the two messages in `packages/protocol/src/ws-messages.ts`. |
| The rules for order, expiry, reconnection, and errors on this page | Authored. Each rule names its source. |
| The `if` and `then` in `envelope` that tie `payload` to the `type` `event` | Authored. The reference does the same check in `packages/protocol/src/ws-messages.ts`, in the schema that its hub and its daemon parse every message with. |
| Every `description` | Authored. |
| The overlap rules on this page | Authored from `apps/hub/src/claims/overlap.ts`. |
| Every expected value in the fixtures | Computed by the schemas, by the `patternsOverlap` and `pathSetsOverlap` functions, by the `Subscription` class of the hub for `deliver`, and by the `OfflineCache` class of the daemon for `active-claims`. The reference does not compute the values of `error-close-codes`: its socket server needs a database, so the extractor reads them from the calls in the source of the server. In `validate-envelope`, the verdict of a message with the `type` `event` comes from the message schema in `ws-messages.ts`, and the verdict of every other message comes from the envelope schema in `envelope.ts`. |

## Intentional differences from the reference

| Difference | Reason |
|---|---|
| Version 1 defines twelve event types. The reference defines nine more: `request`, `artifact`, `ledger.adopted`, `check.timeout-pass`, `handoff.offered`, `handoff.accepted`, `handoff.declined`, `handoff.completed`, and `handoff.cancelled`. A version 1 validator rejects them. | The owner has not decided whether these event types are part of the open-source foundation. |
| Version 1 defines three payloads: the stored event, `resume`, and `error`. The reference defines a payload for each of its message types, and its hub and its daemon reject a message whose `type` is not one of them. A version 1 validator accepts such a message. | The rules on this page need only these three. The messages that carry a post, a claim request, and the proof of who a client is belong to the hub of [issue 19](https://github.com/Multiplexer-si/OpenPlexer/issues/19). |
| A receiver skips a message whose `v` is not 1 (rule O8). The daemon of the reference does not read `v`. | A receiver cannot know what a message of another version means. The hub of the reference sends only `v` 1, so the two behaviors differ on no message of the reference. |
| A receiver reads `at` as a timestamp of RFC 3339 only. The daemon of the reference reads `at` with the date parser of JavaScript, which accepts more forms, such as a date with no time. For an `at` in such a form, a version 1 receiver counts from the time at which it got the event, by rule E9, and the daemon counts from the time that `at` names. | The forms that the date parser of JavaScript accepts beyond its own date-time format depend on the JavaScript engine, so a contract cannot list them. The hub of the reference writes `at` in RFC 3339 only, so the two behaviors differ on no event of the reference. When `at` names the time at which the hub stored the event, a receiver gets the event after that time. The claim of a version 1 receiver then expires later than the claim of the daemon, and the receiver reports the conflict for longer. |
| `right_to_post` always refuses a post for good (rule X5). The daemon of the reference sends the post again unless `message` starts with `agents may not post`. | The wording of `message` is not part of the contract. A post that the hub refuses for the right to post fails the same way when it is sent again. |
| A receiver that skips an event records the `seq` of that event. The daemon of the reference does not. | A receiver that has recorded the `seq` does not ask for the same event after every reconnection. Both behaviors work with the hub of the reference. |
| The Go implementation reports an overlap for a glob whose size is more than 3000. The size is the number of characters of the normalized glob plus the number of states of the automaton that matches it against a path of 4,096 characters, so the limit refuses a plain glob of more than about 1,500 characters. The Go implementation also reports an overlap for any entry, a glob or a path, of more than 4,096 UTF-16 code units, before it matches. A match that exceeds the [policy work budget](../policy/README.md#intentional-differences-from-the-reference) also reports an overlap. The reference has none of these limits. | The time of a match grows with the size of the glob times the length of the path, and the schema puts no limit on the length of an entry. The reference gives no answer on a glob of many `*`: on Node.js 22.16, `*a` 14 times and then `c`, against 28 `a`, takes 0.3 s, and the same glob with `*a` 1,500 times gives no answer in 20 s. A false overlap costs little, because a claim is a warning. |
| A receiver keeps an unknown field of `agent`, of `sender`, and of `needs`. The reference drops an unknown field of these three objects when it parses a message, and it keeps an unknown field of an event, of a body, and of an envelope. | [The general rule](../README.md#writers-and-receivers) has no exception. No conforming writer sends such a field today, so the two behaviors differ only after version 1 adds a field to one of the three objects. |

No fixture case depends on one of these differences. In the fixture `active-claims`, every `at` is a timestamp of RFC 3339, except in two cases with an `at` that neither a version 1 receiver nor the reference can read. Every expected value is the value that the reference returns. One case needs a note. In `validate-envelope`, the case `a type that no message defines is valid` records the verdict of the envelope schema of the reference. The message schema of the reference, which its hub and its daemon parse with, rejects that message.

## Defaults that wait for the operator

The [trust model](../../docs/trust-model.md) gave three decisions about this contract to the owner. On 2026-11-18 the operator decided each one as below. Each choice keeps the contract smallest, and each one fails closed: a version 1 writer does not send the data, and a version 1 receiver skips it.

| Decision | Decided on 2026-11-18 | Reason |
|---|---|---|
| Whether read receipts are part of the Room contract | They are not. Version 1 has no message and no field for a receipt, and a version 1 agent sends none. | A receipt tells the hub which events reached an agent, which is more data that leaves the machine. The reference sends receipts outside the event stream, in the digest and in an HTTP call, so none of the rules above needs one. A later version can add them without a change to an event. |
| The event types `request`, `handoff.*`, `artifact`, `ledger.adopted`, and `check.timeout-pass` | They are not in version 1. A validator rejects them, and a receiver skips and counts them by rule O7. | Each type adds data that leaves the machine. A receiver that skips an unknown type keeps working when a type is added later. A type that version 1 had would be hard to remove. |
| The wire format of the digest | Version 1 defines none. It has no `digest.get` and no `digest` message. An agent gets Room text only from events that it validated. | The digest is text that the hub writes and the model reads, so it is the path for an injected instruction. Without it, every piece of Room text that reaches an agent has passed the event schema. |

A receiver skips a message with the type `digest`, `digest.get`, or `receipt`, and it keeps the connection. It records no `seq` for such a message, because only an event has a place in the order of a Room. It skips and counts an event of an undecided type by rule O7.

## Open questions

Each item is behavior of the reference that looks unintended, or a choice that the owner has not made. Version 1 keeps the behavior of the reference until the owner decides.

1. **May version 1 gain an event type?** A receiver skips an event of an unknown type, so a new type does not stop an older receiver. The older receiver does not see the new events, and an older validator reports them as invalid. Under the general rule a new value in a closed list needs a new version, so each of the nine event types above needs a new version of the contract if it is added after the first release. The owner decides whether the list of event types is an exception.
2. **`at` is any string that is not empty.** The reference does not check that it is a timestamp.
3. **A claim with an empty `paths` list is valid.** It overlaps nothing, so it protects nothing.
4. **`action` in a `policy.denied` body, and `actions` and `scope` in a `policy.override` body, are open strings.** The policy contract has closed lists for the same values.
5. **The envelope schema accepts any `v` greater than zero and any `type` that is not empty. The reference is stricter.** Its hub and its daemon parse every message with a schema that lists their message types. A message with another `type`, or with a payload that does not fit its `type`, fails. The hub answers such a message with an `error` message, the daemon drops it, and both keep the connection. The hub closes a connection that sends a `v` other than 1. Version 1 defines the payloads of `event`, `resume`, and `error`, and rule O8 says what a receiver does with another `v`. What the hub does with a `type` that it does not take is settled with the hub in [issue 19](https://github.com/Multiplexer-si/OpenPlexer/issues/19).
6. **The reference removes the white space around `text` and `addressed_to` when it parses a `question` or an `answer`.** Version 1 says only that the value needs one character that is not white space. It does not say whether a receiver stores the trimmed value.
7. **Some classes make the overlap check of the reference fail, or give a result that the glob rules do not.** The reference copies the text of a class into an ECMA-262 regular expression without the `i` flag and without the `u` flag. Two kinds of class show it.
    - A range that is out of order, such as `[z-a]`, makes the reference throw an error.
    - In a class whose first character is `]`, ECMA-262 reads `[]` as a class that matches nothing, and it reads the rest of the class, with its `]`, as regular expression syntax. If the rest holds none of the syntax characters `^ $ . * + ? ( ) [ { } |`, the glob matches no path: `src/[]x]` matches neither `src/x` nor `src/]`. Otherwise the reference can match paths that the glob does not seem to describe, or throw. `a[]*]` matches `a]`. `ab[]|c]` matches every path that ends in `c]`, because the `|` splits the whole glob in two alternatives, and only the first is held to the start of the path. `[](]` and `[]**]` make the reference throw.

    Version 1 keeps the result of the reference wherever the reference returns one. For a class whose first character is `]`, an implementation must therefore read the rest of the class as the regular expression that the reference builds. Where the reference throws, the two entries overlap.

    The reference reads a glob only when it needs it, and it stops at the first test that holds. So it returns an overlap for `**` and `[z-a]` in that order, because `**` matches the empty literal prefix of `[z-a]`, and it throws for the same two entries in the other order. Both results are an overlap in version 1, so the order of two entries never changes the result.

    Version 1 does not define the result in these cases. The Go implementation reports an overlap in each one.
    - The rest of the class holds a group that starts with `(?`, and the group is not one of `(?:`, `(?=`, `(?!`, `(?<=`, `(?<!`, and a group with a name of ASCII letters, digits, `$`, and `_`, such as `(?<n>`. A group with flags, such as `(?i:`, is an example. The result of the reference depends on the version of its JavaScript engine.
    - The glob holds more than 1000 groups.
    - The rest of the class holds a count with a large minimum, and the group that it counts can match the empty string, as in `(?:a*){10000000}`. On Node.js 22.16 the reference returns a result for a minimum of 1,000,000, and it throws `Maximum call stack size exceeded` for a minimum of 10,000,000.
    - The glob is too large to match. [The differences from the reference](#intentional-differences-from-the-reference) give the limit of the Go implementation.

    The fixture `claims-patterns-overlap` pins the classes that match no path. The fixture `claims-class-syntax` pins the classes that match a path, and it records the cases where the reference throws with the expected value `{"throws": true}`. The fixture `claims-path-sets-overlap` holds two lists with a glob that throws. No fixture covers the cases that version 1 does not define, and the tests of `internal/room` pin what the Go implementation does with them.
8. **`dir/**` matches `dir` in the policy dialect and not in the claims dialect.** A claim on `src/**` does not overlap a claim on the path `src`.
