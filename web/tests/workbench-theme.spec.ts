import { test, expect } from "@playwright/test";
import { mkdir } from "node:fs/promises";
import { join } from "node:path";

test("login defaults to a CSP-safe dark theme and persists an explicit choice", async ({
  page,
}) => {
  const failures: string[] = [];
  page.on("pageerror", (error) => failures.push(error.message));
  await page.goto("/");
  await expect(
    page.getByRole("heading", { name: "进入研发工作台" }),
  ).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute(
    "data-mantine-color-scheme",
    "dark",
  );
  await page
    .getByRole("button", { name: "切换为浅色主题", exact: true })
    .click();
  await expect(page.locator("html")).toHaveAttribute(
    "data-mantine-color-scheme",
    "light",
  );
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute(
    "data-mantine-color-scheme",
    "light",
  );
  await expect(
    page.getByRole("button", { name: "切换为深色主题", exact: true }),
  ).toBeVisible();
  expect(
    await page
      .locator("body")
      .evaluate((element) => getComputedStyle(element).fontFamily),
  ).not.toContain("Kevin");
  expect(failures).toEqual([]);
});

test("a stored light preference is applied before the application bundle", async ({
  page,
}) => {
  await page.addInitScript(() => {
    localStorage.setItem("is-workbench-color-scheme", "light");
  });
  let releaseBundle: () => void = () => {};
  const bundleReady = new Promise<void>((resolve) => {
    releaseBundle = resolve;
  });
  await page.route("**/assets/*.js", async (route) => {
    await bundleReady;
    await route.continue();
  });
  try {
    await page.goto("/", { waitUntil: "commit" });
    await expect(page.locator("html")).toHaveAttribute(
      "data-mantine-color-scheme",
      "light",
    );
  } finally {
    releaseBundle();
  }
  await expect(
    page.getByRole("heading", { name: "进入研发工作台" }),
  ).toBeVisible();
});

test("the selected login theme renders without specialist bundles or external requests", async ({
  page,
}) => {
  const selected =
    process.env.IS_WORKBENCH_THEME === "light" ? "light" : "dark";
  const scripts: string[] = [];
  const external: string[] = [];
  const errors: string[] = [];
  await page.addInitScript(
    (scheme) => localStorage.setItem("is-workbench-color-scheme", scheme),
    selected,
  );
  page.on("request", (request) => {
    if (request.resourceType() === "script") scripts.push(request.url());
    if (
      /^https?:/.test(request.url()) &&
      !request.url().startsWith("http://127.0.0.1:18082/")
    )
      external.push(request.url());
  });
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto("/");
  await expect(
    page.getByRole("heading", { name: "进入研发工作台" }),
  ).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute(
    "data-mantine-color-scheme",
    selected,
  );
  expect(
    scripts.filter((url) =>
      /TaskGraph|Charts-|SpecialistContent|Delivery-|Engineering-|Operations-/.test(
        url,
      ),
    ),
  ).toEqual([]);
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth + 1,
    ),
  ).toBeTruthy();
  const directory = process.env.IS_WORKBENCH_SCREENSHOTS;
  if (directory) {
    await mkdir(directory, { recursive: true });
    await page.screenshot({
      path: join(directory, "登录.png"),
      fullPage: true,
      animations: "disabled",
    });
  }
});
