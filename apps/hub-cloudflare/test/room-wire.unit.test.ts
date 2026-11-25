import { describe, expect, test } from "vitest";
import envelopes from "../../../spec/room/v1/fixtures/validate-envelope.json?raw";
import events from "../../../spec/room/v1/fixtures/validate-room-event.json?raw";
import type { JsonValue } from "../src/canonical";
import { decodeRoomJSON, encodeRoomJSON, parseEventPost, validateEnvelope, validateEvent } from "../src/room-wire";

type Case = { name: string; input: JsonValue; expected: { valid: boolean } };

const utf8 = new TextEncoder();

for (const [name, source, validate] of [
  ["validate-room-event", events, validateEvent],
  ["validate-envelope", envelopes, validateEnvelope],
] as const) {
  const fixtures: { cases: Case[] } = JSON.parse(source);
  describe(name, () => {
    test.each(fixtures.cases)("$name", ({ input, expected }) => {
      const check = () => validate(decodeRoomJSON(utf8.encode(JSON.stringify(input))));
      if (expected.valid) expect(check).not.toThrow();
      else expect(check).toThrow(SyntaxError);
    });
  });
}

test("accepts exact Room fields and the last repeated member, and preserves extension numbers", () => {
  const body = '"body":{"summary":"working","extension":{"n":9007199254740993,"overflow":1e400,"s":"\\ud800123"}}';
  const post = parseEventPost(utf8.encode('{"type":"status","body":false,' + body + '}'));
  expect(encodeRoomJSON(post.body)).toBe(
    '{"extension":{"n":9007199254740993,"overflow":1e400,"s":"�123"},"summary":"working"}',
  );
  expect(() => parseEventPost(utf8.encode('{"Type":"status","body":{}}'))).toThrow("type is required");
  expect(() => parseEventPost(utf8.encode('{"type":"status","body":{"Summary":""}}'))).toThrow("body.summary is required");
  expect(() => parseEventPost(utf8.encode('{"type":"status","body":{"summary":1}}'))).toThrow("body.summary must be a string");
});

test("reports invalid UTF-8, malformed JSON, and trailing text separately", () => {
  expect(() => decodeRoomJSON(Uint8Array.of(0xff))).toThrow("is not valid UTF-8");
  expect(() => decodeRoomJSON(utf8.encode('{"type"'))).toThrow("is not valid JSON");
  expect(() => decodeRoomJSON(utf8.encode('{} {}'))).toThrow("has text after the JSON value");
});

test("keeps deep extension arrays within the Room JSON limit", () => {
  const body = '{"type":"status","body":{"summary":"","extension":';
  const n = 9997;
  const extension = "[".repeat(n) + "1" + "]".repeat(n);
  const post = parseEventPost(utf8.encode(body + extension + "}}"));
  expect(encodeRoomJSON(post.body)).toBe('{"extension":' + extension + ',"summary":""}');
  expect(() => parseEventPost(utf8.encode(body + "[[" + extension + "]]}}"))).toThrow("is not valid JSON");
});
