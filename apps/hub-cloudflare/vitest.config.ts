import { cloudflareTest } from "@cloudflare/vitest-pool-workers";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [cloudflareTest({
    wrangler: { configPath: "wrangler.toml" },
    remoteBindings: false,
    // The pinned test pool bundles the August workerd. Wrangler deploy uses the November date.
    miniflare: { compatibilityDate: "2026-08-15" },
  })],
});
