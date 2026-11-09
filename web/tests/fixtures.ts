import { test as base, expect, type APIRequestContext } from "./theme";

type OwnerAuth = {
  csrf: string;
  state: Awaited<ReturnType<APIRequestContext["storageState"]>>;
};

// Non-login scenarios share one real owner session per worker. The workbench
// tests still exercise interactive login; production login limits stay enabled.
export const test = base.extend<
  { ownerSession: { csrf: string } },
  { ownerAuth: OwnerAuth }
>({
  ownerAuth: [
    async ({ playwright }, use) => {
      const client = await playwright.request.newContext({
        baseURL: "http://127.0.0.1:18082",
      });
      try {
        const response = await client.post("/api/v1/auth/login", {
          data: {
            email: "browser@example.test",
            password: "browser-test-password-2026",
          },
          headers: { Origin: "http://127.0.0.1:18082" },
        });
        expect(response.status()).toBe(200);
        const session = await response.json();
        await use({ csrf: session.csrf, state: await client.storageState() });
      } finally {
        await client.dispose();
      }
    },
    { scope: "worker" },
  ],
  storageState: async ({ ownerAuth }, use) => {
    await use(ownerAuth.state);
  },
  ownerSession: async ({ ownerAuth }, use) => {
    await use({ csrf: ownerAuth.csrf });
  },
});

export { expect };
