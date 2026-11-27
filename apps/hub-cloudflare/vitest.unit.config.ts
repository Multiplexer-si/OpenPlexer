import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["test/**/*.unit.test.ts"],
    // Node 22.12 needs the flag for the built-in SQLite used by the member tests.
    execArgv: ["--experimental-sqlite"],
  },
});
