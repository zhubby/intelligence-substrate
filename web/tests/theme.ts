import { test as base, expect } from "@playwright/test";

export const test = base.extend({
  browser: [
    async ({ browser }, use) => {
      const createContext = browser.newContext.bind(browser);
      browser.newContext = async (options) => {
        const context = await createContext(options);
        const theme = process.env.IS_WORKBENCH_THEME;
        if (theme === "dark" || theme === "light") {
          await context.addInitScript((preference) => {
            try {
              if (!localStorage.getItem("is-workbench-color-scheme"))
                localStorage.setItem("is-workbench-color-scheme", preference);
            } catch {}
          }, theme);
        }
        return context;
      };
      try {
        await use(browser);
      } finally {
        browser.newContext = createContext;
      }
    },
    { scope: "worker" },
  ],
});

export { expect };
export type { Page, APIRequestContext } from "@playwright/test";
