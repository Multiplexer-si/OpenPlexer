import { HUB_ERROR_SOURCES } from "../hub-errors.ts";
import type { Reference } from "../reference.ts";
import { fixtureFile, fixturePath, type Case, type FixtureFile } from "./format.ts";

export function roomErrorFixtures(reference: Reference): [string, FixtureFile][] {
  const cases = [...reference.hubErrorCodes].map(([code, closeCode]): Case => {
    if (closeCode === null) {
      return { name: `the hub keeps the connection after ${code}`, why: "The hub sends this code with a function that keeps the connection.", input: { code }, expected: { close_code: null } };
    }
    return { name: `the hub closes with ${closeCode} after ${code}`, why: "The hub sends this code with `fatal`, which closes the connection.", input: { code }, expected: { close_code: closeCode } };
  });
  return [
    [
      fixturePath("room", "error-close-codes"),
      fixtureFile({
        contract: "room",
        operation: "close-code",
        description:
          "The close code that follows each error code that the socket server of the hub names. The extractor reads the calls of `fatal`, `error`, `postError`, and `claimErr` in the source of the server, because the server does not run without a database.",
        sources: HUB_ERROR_SOURCES,
        cases,
      }),
    ],
  ];
}
