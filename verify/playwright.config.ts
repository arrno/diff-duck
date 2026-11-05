import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  testMatch: ["viewer.spec.ts", "app.spec.ts"],
  fullyParallel: true,
  reporter: [["list"]],
  outputDir: process.env.VERIFY_WORK + "/test-results",
  use: { browserName: "chromium" },
});
