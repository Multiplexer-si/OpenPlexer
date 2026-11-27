import { randomBytes } from "node:crypto";
import { cloudflareTest } from "@cloudflare/vitest-pool-workers";
import { configDefaults, defineConfig } from "vitest/config";

export default defineConfig({
  test: { exclude: [...configDefaults.exclude, "test/**/*.node.unit.test.ts"] },
  plugins: [cloudflareTest({
    wrangler: { configPath: "wrangler.toml" },
    remoteBindings: false,
    // The pinned test pool bundles the August workerd. Wrangler deploy uses the November date.
    miniflare: {
      compatibilityDate: "2026-08-15",
      bindings: {
        BOOTSTRAP_TOKEN: randomBytes(32).toString("base64url"),
        WRAP_KEY_V1: randomBytes(32).toString("hex"),
      },
    },
  })],
});
