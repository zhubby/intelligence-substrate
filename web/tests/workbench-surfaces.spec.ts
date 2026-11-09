import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import type { Page } from "@playwright/test";
import { test, expect } from "./fixtures";

async function capture(page: Page, name: string) {
  const directory = process.env.IS_WORKBENCH_SCREENSHOTS;
  if (!directory) return;
  await mkdir(directory, { recursive: true });
  await page.screenshot({
    path: join(directory, name + ".png"),
    fullPage: true,
    animations: "disabled",
  });
}

async function navigate(page: Page, title: string) {
  const menu = page.getByRole("button", { name: "打开导航", exact: true });
  if (await menu.isVisible()) await menu.click();
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("button", { name: title, exact: true })
    .click();
}

async function createWorkspace(page: Page, csrf: string) {
  const command = async (name: string, data: unknown) => {
    const response = await page.request.post("/api/v1/commands/" + name, {
      data,
      headers: { Origin: "http://127.0.0.1:18082", "X-CSRF-Token": csrf },
    });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  const project = await command("projects.create", {
    title: "双主题交付控制台",
  });
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "客户预约的受控交付",
    outcome: "客户可以预约服务，取消本人预约，并看到准确状态。",
    targets: ["web"],
    criteria: [{ id: "own-booking", text: "客户只能取消本人预约。" }],
    budget: 100,
  });
  await command("goals.approve", { id: goal.id });
  const plan = await command("plans.propose", {
    goalId: goal.id,
    summary: "明确预约契约并独立复核，保留人工审批。",
    tasks: [
      {
        id: "analysis",
        title: "分析预约契约",
        kind: "analysis",
        role: "analysis",
        runtime: "linux",
      },
      {
        id: "review",
        title: "复核预约边界",
        kind: "analysis",
        role: "architect",
        runtime: "linux",
        dependsOn: ["analysis"],
      },
    ],
  });
  await command("plans.approve", { id: plan.id });
  await page.goto("/");
  const menu = page.getByRole("button", { name: "打开导航", exact: true });
  if (await menu.isVisible()) await menu.click();
  await page.getByLabel("选择项目").selectOption(project.id);
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("button", { name: /^目标与需求/ })
    .click();
  await page.getByLabel("选择交付目标").selectOption(goal.id);
  return { command, project, goal, plan };
}

test("read-only plan and execution graphs reuse exact task details without writing commands", async ({
  page,
  ownerSession,
}) => {
  await createWorkspace(page, ownerSession.csrf);
  await expect(page.locator("html")).toHaveAttribute(
    "data-mantine-color-scheme",
    process.env.IS_WORKBENCH_THEME || "dark",
  );
  await page.getByRole("tab", { name: "交付方案", exact: true }).click();
  const presentation = page.getByRole("radiogroup", {
    name: "交付方案 v1展示方式",
  });
  await expect(
    presentation.getByRole("radio", { name: "列表", exact: true }),
  ).toBeChecked();
  const writes: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().includes("/commands/"))
      writes.push(request.url());
  });
  await presentation
    .getByRole("radio", { name: "依赖图", exact: true })
    .check();
  const graph = page.getByRole("region", { name: "方案依赖图", exact: true });
  await expect(graph).toBeVisible();
  await expect(
    graph.getByRole("button", {
      name: "查看工作项：分析预约契约",
      exact: true,
    }),
  ).toBeVisible();
  await expect(graph.locator(".react-flow__edge")).toHaveCount(1);
  await capture(page, "方案依赖图");
  await graph
    .getByRole("button", { name: "查看工作项：分析预约契约", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toContainText("待执行");
  await capture(page, "工作项详情");
  await page.keyboard.press("Escape");
  await page.getByRole("tab", { name: "执行记录", exact: true }).click();
  const execution = page.getByRole("radiogroup", {
    name: "执行工作项展示方式",
  });
  await expect(
    execution.getByRole("radio", { name: "列表", exact: true }),
  ).toBeChecked();
  await execution.getByRole("radio", { name: "依赖图", exact: true }).check();
  await expect(
    page
      .getByRole("region", { name: "执行依赖图", exact: true })
      .locator(".react-flow__edge"),
  ).toHaveCount(1);
  await capture(page, "执行依赖图");
  expect(writes).toEqual([]);
});

test("all existing entries render responsively without CSP or external resource requests", async ({
  page,
  ownerSession,
}, info) => {
  const failures: string[] = [];
  const external: string[] = [];
  page.on("pageerror", (error) => failures.push(error.message));
  page.on("request", (request) => {
    if (
      /^https?:/.test(request.url()) &&
      !request.url().startsWith("http://127.0.0.1:18082/")
    )
      external.push(request.url());
  });
  await page.addInitScript(() => {
    document.addEventListener("securitypolicyviolation", (event) => {
      document.documentElement.dataset.cspFailure = event.violatedDirective;
    });
  });
  await createWorkspace(page, ownerSession.csrf);
  const screenshotDirectory = process.env.IS_WORKBENCH_SCREENSHOTS;
  if (screenshotDirectory)
    await mkdir(screenshotDirectory, { recursive: true });
  for (const title of [
    "目标与需求",
    "代码与评审",
    "质量与证据",
    "构建与发布",
    "Agent 与执行",
    "企业治理",
  ]) {
    await navigate(page, title);
    await expect(page.locator("#main-content")).toBeVisible();
    if (title === "Agent 与执行")
      await expect(page.locator(".tremor-bar-list")).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth + 1,
      ),
      title,
    ).toBeTruthy();
    await expect(page.locator("html")).not.toHaveAttribute(
      "data-csp-failure",
      /.+/,
    );
    if (screenshotDirectory) {
      await page.screenshot({
        path: join(screenshotDirectory, title + ".png"),
        fullPage: true,
        animations: "disabled",
      });
    }
  }
  await page.emulateMedia({ reducedMotion: "reduce" });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth + 1,
    ),
  ).toBeTruthy();
  expect(failures).toEqual([]);
  expect(external).toEqual([]);
  expect(info.project.name).toBeTruthy();
});

test("switching projects resets graph selection and never exposes another project's tasks", async ({
  page,
  ownerSession,
}) => {
  const first = await createWorkspace(page, ownerSession.csrf);
  const other = await first.command("projects.create", {
    title: "隔离项目，不含原工作项",
  });
  await page.reload();
  const toggle = page.getByRole("button", { name: "打开导航", exact: true });
  if (await toggle.isVisible()) await toggle.click();
  await page.getByLabel("选择项目").selectOption(first.project.id);
  const close = page.getByRole("button", { name: "关闭导航", exact: true });
  if (await close.isVisible()) await close.click();
  await page.getByLabel("选择交付目标").selectOption(first.goal.id);
  await page.getByRole("tab", { name: "交付方案", exact: true }).click();
  await page
    .getByRole("radiogroup", { name: "交付方案 v1展示方式" })
    .getByRole("radio", { name: "依赖图", exact: true })
    .check();
  await expect(
    page.getByRole("region", { name: "方案依赖图", exact: true }),
  ).toBeVisible();
  if (await toggle.isVisible()) await toggle.click();
  await page.getByLabel("选择项目").selectOption(other.id);
  if (await close.isVisible()) await close.click();
  await expect(
    page.getByRole("heading", {
      name: "从一个明确的业务目标开始",
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page.getByRole("region", { name: "方案依赖图", exact: true }),
  ).toHaveCount(0);
  await expect(page.getByText("分析预约契约", { exact: true })).toHaveCount(0);
  if (await toggle.isVisible()) await toggle.click();
  await page.getByLabel("选择项目").selectOption(first.project.id);
  if (await close.isVisible()) await close.click();
  await page.getByRole("tab", { name: "交付方案", exact: true }).click();
  await expect(
    page
      .getByRole("radiogroup", { name: "交付方案 v1展示方式" })
      .getByRole("radio", { name: "列表", exact: true }),
  ).toBeChecked();
});

test("keyboard, modal focus, mobile isolation, long content and 200 percent zoom remain usable", async ({
  page,
  ownerSession,
}, info) => {
  const workspace = await createWorkspace(page, ownerSession.csrf);
  await workspace.command("knowledge.create", {
    projectId: workspace.project.id,
    title: "长中文验证".repeat(12),
    content: "交付依据".repeat(100) + "\n" + "a".repeat(128),
    source: "浏览器布局验证",
  });
  const newGoal = page.getByRole("button", {
    name: "新建交付目标",
    exact: true,
  });
  await newGoal.focus();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  const bounds = (await dialog.boundingBox())!;
  expect(bounds.x).toBeGreaterThanOrEqual(0);
  expect(bounds.x + bounds.width).toBeLessThanOrEqual(
    page.viewportSize()!.width + 1,
  );
  await capture(page, "表单与弹窗");
  await page.keyboard.press("Tab");
  expect(
    await dialog.evaluate((element) =>
      element.contains(document.activeElement),
    ),
  ).toBeTruthy();
  await page.keyboard.press("Escape");
  await expect(dialog).not.toBeVisible();
  await expect(newGoal).toBeFocused();
  if (info.project.name === "mobile") {
    const menu = page.getByRole("button", { name: "打开导航", exact: true });
    await menu.click();
    await expect(page.locator(".main-shell")).toHaveAttribute("inert", "");
    await page.getByRole("button", { name: "关闭导航", exact: true }).focus();
    await page.keyboard.press("Shift+Tab");
    expect(
      await page
        .locator(".sidebar")
        .evaluate((element) => element.contains(document.activeElement)),
    ).toBeTruthy();
    await page.keyboard.press("Escape");
    await expect(menu).toBeFocused();
    await expect(page.locator(".sidebar")).toHaveAttribute("inert", "");
  }
  await navigate(page, "企业治理");
  const policies = page.getByRole("tab", { name: "控制策略", exact: true });
  await policies.focus();
  await page.keyboard.press("ArrowLeft");
  await expect(
    page.getByRole("tab", { name: "项目知识", exact: true }),
  ).toBeFocused();
  await page.keyboard.press("Enter");
  await page.getByRole("button", { name: /^长中文验证/ }).click();
  await expect(dialog).toBeVisible();
  expect(
    await dialog.evaluate(
      (element) => element.scrollWidth <= element.clientWidth + 1,
    ),
  ).toBeTruthy();
  await page.keyboard.press("Escape");
  const viewport = page.viewportSize()!;
  await page.setViewportSize({
    width: Math.round(viewport.width / 2),
    height: Math.round(viewport.height / 2),
  });
  await page.emulateMedia({ reducedMotion: "reduce" });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth + 1,
    ),
  ).toBeTruthy();
  await page.getByRole("tab", { name: "交付效能", exact: true }).click();
  await expect(
    page.getByRole("group", { name: "人工对照基线", exact: true }),
  ).toContainText("待采集");
  await expect(page.locator(".tremor-bar-list")).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth + 1,
    ),
  ).toBeTruthy();
  await capture(page, "两倍缩放等效布局");
});
