import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./tests",
  // npm run test:e2e includes these with dedicated local model fixtures.
  testIgnore: [
    "**/clarifications.spec.ts",
    "**/delivery-costs.spec.ts",
    "**/requirement-drafts.spec.ts",
    "**/plan-generation.spec.ts",
    "**/designs.spec.ts",
    "**/blueprints.spec.ts",
  ],
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: "list",
  timeout: 60000,
  use: {
    baseURL: "http://127.0.0.1:18082",
    trace: "retain-on-failure",
    launchOptions: {
      executablePath: process.env.IS_BROWSER_EXECUTABLE || undefined,
    },
  },
  webServer: {
    command:
      (process.env.CI ? "" : "rtk proxy ") +
      "go run ./cmd/platform -listen 127.0.0.1:18082 -data work/e2e-state",
    cwd: "..",
    url: "http://127.0.0.1:18082/healthz",
    reuseExistingServer: false,
    timeout: 120000,
    env: {
      IS_DEV_MODE: "true",
      IS_BOOTSTRAP_EMAIL: "browser@example.test",
      IS_BOOTSTRAP_PASSWORD: "browser-test-password-2026",
      IS_MODEL_BASE_URL: "",
      IS_MODEL_NAME: "",
      IS_OIDC_ISSUER: "",
    },
  },
  projects: [
    {
      name: "desktop",
      use: { browserName: "chromium", viewport: { width: 1440, height: 1000 } },
    },
    {
      name: "mobile",
      use: {
        browserName: "chromium",
        viewport: { width: 390, height: 844 },
        isMobile: true,
        hasTouch: true,
      },
    },
  ],
});
