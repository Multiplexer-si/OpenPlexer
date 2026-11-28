import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { URL } from "node:url";
import { expect, test } from "vitest";

test.each([
  ["200", 0],
  ["201", 1],
])("conformance health check handles HTTP %s with the expected body", (http, expected) => {
  const script = readFileSync(new URL("../scripts/conformance.sh", import.meta.url), "utf8");
  const healthCheck = script.match(/^check_health\(\) \{\n[\s\S]*?^\}/m)?.[0];
  if (!healthCheck) throw new Error("Missing conformance health check");
  const state = mkdtempSync(join(tmpdir(), "openplexer-health."));
  try {
    const result = spawnSync("bash", ["-euc", `
      state=$1
      http=$2
      wrangler_pid=$$
      origin=http://127.0.0.1:8787
      curl() {
        while [ "$#" -gt 0 ]; do
          case "$1" in
            -o) printf '%s' '{"ok":true,"schema":5}' > "$2"; shift ;;
            -w) printf '%s' "$http"; shift ;;
          esac
          shift
        done
      }
      sleep() { SECONDS=$((SECONDS + 60)); }
      ${healthCheck}
      check_health
    `, "health-check", state, http], {
      env: { PATH: process.env.PATH, HOME: state },
      encoding: "utf8",
      timeout: 5_000,
    });
    expect(result.error).toBeUndefined();
    expect(result.status, result.stderr).toBe(expected);
  } finally {
    rmSync(state, { recursive: true, force: true });
  }
});
