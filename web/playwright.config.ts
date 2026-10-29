import { defineConfig, devices } from "@playwright/test";

// The browser tests run against the local environment, started with
// `docker compose up --build --wait`, as CI does. They run one at a time:
// some switch the endpoint or the acquirer off, which every test shares.
export default defineConfig({
  testDir: "./e2e",
  timeout: 90_000,
  expect: { timeout: 15_000 },
  workers: 1,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  use: {
    ...devices["Desktop Chrome"],
    baseURL: process.env.BASE_URL ?? "http://localhost:3000",
    trace: "retain-on-failure",
    // PW_CHANNEL=chrome runs the installed Chrome, which saves a download.
    channel: process.env.PW_CHANNEL || undefined,
  },
});
