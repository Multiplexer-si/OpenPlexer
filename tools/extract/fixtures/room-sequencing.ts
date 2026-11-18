import { exampleEvent } from "../examples.ts";
import { asCommitted, type Json } from "../output.ts";
import type { Reference } from "../reference.ts";
import { fixtureFile, fixturePath, type Case, type FixtureFile } from "./format.ts";

type Step = ["replay" | "live", number] | "go-live";

const DELIVERIES: [name: string, why: string, since: number, steps: Step[]][] = [
  ["a replay is sent in order", "A replayed event after `since` is sent at once.", 0, [["replay", 1], ["replay", 2], ["replay", 3], "go-live"]],
  ["a replayed event at or below since is not sent", "The subscription starts after `since`.", 5, [["replay", 4], ["replay", 5], ["replay", 6], "go-live"]],
  ["a live event waits for the end of the replay", "A live event that arrives during the replay is held, so that it is not sent before an older event.", 0, [["live", 3], ["replay", 1], ["replay", 2], "go-live"]],
  ["a live event that the replay also read is sent once", "The replay and the live events can overlap. An event at or below the last one sent is dropped.", 0, [["live", 2], ["live", 3], ["replay", 1], ["replay", 2], "go-live"]],
  ["a live event is sent at once after the replay", "After the replay ends, nothing is held.", 0, [["replay", 1], "go-live", ["live", 2], ["live", 3]]],
  ["a live event that arrives twice is sent once", "The hub can read one event twice, after a notification and after a poll.", 0, ["go-live", ["live", 1], ["live", 1], ["live", 2]]],
  ["a live event that arrives late is dropped", "An event below the last one sent is never sent, so the order on the connection never goes back.", 0, ["go-live", ["live", 2], ["live", 1], ["live", 3]]],
  ["a gap is not filled", "The subscription does not wait for a missing `seq`.", 0, ["go-live", ["live", 1], ["live", 4]]],
  ["a since above the last event drops every event up to it", "The hub accepts a `last_seq` above the last event of the Room, and the subscription then drops every event at or below it.", 9, [["replay", 7], "go-live", ["live", 8], ["live", 9], ["live", 10]]],
  ["nothing is sent before the end of the replay if only live events arrive", "Without the end of the replay, a live event stays held.", 0, [["live", 1], ["live", 2]]],
  ["no step sends nothing", "A subscription with no event keeps `since` as its last `seq`.", 12, ["go-live"]],
];

const AT = "2026-01-15T12:00:00.000Z";
const time = (text: string): number => Date.parse(text);

function claim(seq: number, body: { [key: string]: Json }, more: { [key: string]: Json } = {}): { [key: string]: Json } {
  return { ...exampleEvent("claim", { item: "Cart discount", paths: ["src/orders/**"], intent: "Add discounts", ttl_seconds: 600, ...body }, seq), ...more };
}

function release(seq: number, claimId: string, more: { [key: string]: Json } = {}): { [key: string]: Json } {
  return { ...exampleEvent("release", { claim_id: claimId }, seq), ...more };
}

const other = { agent: { id: "ses-ben-1", vendor: "codex" }, on_behalf_of: "mem-ben" };

const CLAIMS: [name: string, why: string, events: Json[], now: string][] = [
  ["a claim is active before it expires", "A claim expires `ttl_seconds` after the `at` of its event.", [claim(1, {})], "2026-01-15T12:09:59.999Z"],
  ["a claim is not active at the time that it expires", "A claim is active only while its expiry is later than the current time.", [claim(1, {})], "2026-01-15T12:10:00.000Z"],
  ["a claim is not active after it expires", "No event ends an expired claim on the receiver. The receiver stops listing it.", [claim(1, {})], "2026-01-15T12:30:00.000Z"],
  ["the id of a claim is the claim_id of its body", "The hub of the reference puts the id of the claim in `claim_id`, which the body schema does not name.", [claim(1, { claim_id: "clm-1" })], AT],
  ["the id of a claim without claim_id is the id of its event", "A claim event with no `claim_id` is named by its event.", [claim(1, {})], AT],
  ["a claim_id that is not a string is ignored", "Only a string names a claim.", [claim(1, { claim_id: 7 })], AT],
  ["a release from the holder ends the claim", "A `release` event names the claim in `claim_id`.", [claim(1, { claim_id: "clm-1" }), release(2, "clm-1")], AT],
  ["a release from another agent session does not end the claim", "Only the agent session that holds a claim can release it.", [claim(1, { claim_id: "clm-1" }), release(2, "clm-1", other)], AT],
  ["a release of an unknown claim changes nothing", "A release that names no held claim is ignored.", [claim(1, { claim_id: "clm-1" }), release(2, "clm-9")], AT],
  ["a release with the outcome expired ends the claim", "The hub of the reference posts this release, in the name of the holder, when it finds an expired claim.", [claim(1, { claim_id: "clm-1" }), { ...exampleEvent("release", { claim_id: "clm-1", outcome: "expired" }, 2) }], AT],
  [
    "claims are listed in the order of their events",
    "The list keeps the order in which the claims arrived.",
    [claim(1, { claim_id: "clm-1" }), claim(2, { claim_id: "clm-2", paths: ["docs/**"], intent: "Write the guide" }, other), claim(3, { claim_id: "clm-3", paths: [] })],
    AT,
  ],
  [
    "a second claim with the same id replaces the first",
    "The later event wins, and the claim moves to the end of the list.",
    [claim(1, { claim_id: "clm-1" }), claim(2, { claim_id: "clm-2" }, other), claim(3, { claim_id: "clm-1", paths: ["src/billing/**"], ttl_seconds: 60 }, { at: "2026-01-15T12:05:00.000Z" })],
    "2026-01-15T12:05:30.000Z",
  ],
  ["only the expired claims leave the list", "Each claim has its own expiry.", [claim(1, { claim_id: "clm-1", ttl_seconds: 60 }), claim(2, { claim_id: "clm-2", ttl_seconds: 3600 }, other)], "2026-01-15T12:01:00.000Z"],
  ["an at with a UTC offset is read as that instant", "`at` is a timestamp of RFC 3339, and the expiry is written in UTC.", [claim(1, {}, { at: "2026-01-15T13:00:00.000+01:00" })], AT],
  ["an at with no fraction is read", "The fraction of a second is optional.", [claim(1, {}, { at: "2026-01-15T12:00:00Z" })], AT],
  ["a claim whose at is not a timestamp counts from the time that it arrives", "The receiver cannot read `at`, so it counts `ttl_seconds` from its own clock, which reads `now` when it gets the event.", [claim(1, {}, { at: "yesterday" })], AT],
  ["a claim whose at is not a timestamp is active for ttl_seconds", "The claim expires `ttl_seconds` after `now`, whatever its `at` says.", [claim(1, { ttl_seconds: 1 }, { at: "not a time" })], "2026-03-01T08:00:00.000Z"],
  ["the expiry has milliseconds", "The expiry is `at` plus `ttl_seconds`, written with three digits of fraction.", [claim(1, { ttl_seconds: 1 }, { at: "2026-01-15T12:00:00.250Z" })], AT],
  ["the claim of a sub-agent is held by the sub-agent session", "The holder is `agent.id` of the claim event, not `agent.parent`.", [claim(1, {}, { agent: { id: "ses-ana-1-sub", vendor: "claude-code", parent: "ses-ana-1" } }), release(2, "evt-0001")], AT],
  ["an event of another type changes nothing", "Only `claim` and `release` events change the claims.", [claim(1, { claim_id: "clm-1" }), exampleEvent("status", { summary: "Running the cart tests", claim_id: "clm-1" }, 2)], AT],
  ["no event gives no claim", "The list starts empty.", [], AT],
];

export function roomSequencingFixtures(reference: Reference): [string, FixtureFile][] {
  const deliveries = DELIVERIES.map(([name, why, since, steps]): Case => {
    const sent: number[] = [];
    const subscription = new reference.Subscription("room-fixture", "daemon", since, (message) => sent.push(message.seq));
    for (const step of steps) {
      if (step === "go-live") subscription.goLive();
      else {
        const row = { seq: step[1], flagged: false, envelope: { seq: step[1] } };
        if (step[0] === "replay") subscription.replay(row);
        else subscription.push(row);
      }
    }
    const input = { since, steps: steps.map((step): Json => (step === "go-live" ? { step } : { step: step[0], seq: step[1] })) };
    return { name, why, input, expected: { sent, last_seq: subscription.lastSeq } };
  });
  const claims = CLAIMS.map(([name, why, events, now]): Case => {
    const input = asCommitted({ events, now });
    return { name, why, input, expected: { claims: reference.activeClaims(input.events, time(input.now)) } };
  });
  return [
    [
      fixturePath("room", "deliver"),
      fixtureFile({ contract: "room", operation: "deliver", description: "The events that one subscription of the hub sends, for a replay and live events in several orders.", sources: ["apps/hub/src/ws/subscriptions.ts"], cases: deliveries }),
    ],
    [
      fixturePath("room", "active-claims"),
      fixtureFile({ contract: "room", operation: "active-claims", description: "The claims of one Room that a receiver holds as active after a list of events. The receiver gets every event at `now`.", sources: ["apps/daemon/src/sync/cache.ts"], cases: claims }),
    ],
  ];
}
