import { test, expect } from "./fixtures";
import type { Page } from "@playwright/test";

const origin = "http://127.0.0.1:18082";
async function fixture(page: Page, csrf: string, suffix: string) {
  const command = async (name: string, data: unknown) => {
    const response = await page.request.post("/api/v1/commands/" + name, {
      data,
      headers: { Origin: origin, "X-CSRF-Token": csrf },
    });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  const project = await command("projects.create", {
    title: "设计追溯 " + suffix,
  });
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "客户预约设计",
    outcome: "客户管理自己的预约，首版不接支付。",
    criteria: [{ id: "own", text: "客户只能查看自己的预约" }],
    targets: ["web"],
  });
  await command("goals.approve", { id: goal.id });
  const repo = await command("repositories.create", {
    projectId: project.id,
    title: "预约应用",
  });
  const query = new URLSearchParams({
    goalId: goal.id,
    repositoryId: repo.id,
    commit: repo.data.head,
    target: "web",
    type: "prototype",
    name: "客户原型.html",
  });
  const upload = await page.request.post("/api/v1/artifacts/upload?" + query, {
    data: Buffer.from(
      `<!doctype html><meta charset="utf-8"><button onclick="this.textContent='预约已选择'">选择预约</button><p id="network">等待检查</p><script>fetch('/api/v1/projects').then(()=>document.querySelector('#network').textContent='网络已连接').catch(()=>document.querySelector('#network').textContent='网络已阻止')</script><button onclick="location.href='/blocked-preview-navigation'">尝试页面导航</button>`,
    ),
    headers: {
      Origin: origin,
      "X-CSRF-Token": csrf,
      "Content-Type": "application/octet-stream",
    },
  });
  expect(upload.ok(), await upload.text()).toBeTruthy();
  return { command, project, goal, prototype: await upload.json() };
}
async function open(
  page: Page,
  project: string,
  goal: string,
  mobile: boolean,
) {
  await page.goto("/");
  if (mobile)
    await page.getByRole("button", { name: "打开导航", exact: true }).click();
  await page.getByLabel("选择项目").selectOption(project);
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("button", { name: /^目标与需求/ })
    .click();
  await page.getByLabel("选择交付目标").selectOption(goal);
  await page.getByRole("tab", { name: "交付方案", exact: true }).click();
}
async function fill(page: Page, title: string) {
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("设计名称", { exact: true }).fill(title);
  await dialog
    .getByLabel("设计说明", { exact: true })
    .fill("预约列表与详情；首版不接支付。");
  await dialog
    .getByLabel("关键设计决定", { exact: true })
    .fill("先展示预约状态；保留明确的取消确认。");
  await dialog
    .getByLabel("设计原因", { exact: true })
    .fill("确认主要客户旅程和错误恢复。");
}

async function prototypeDocument(popup: Page) {
  await popup.waitForLoadState("load");
  for (const frame of popup.frames()) {
    if (
      await frame.getByRole("button", { name: "选择预约", exact: true }).count()
    )
      return frame;
  }
  throw new Error("原型未呈现交互内容");
}

test("prototype proposal is selected for generation and frozen only by human plan approval", async ({
  page,
  ownerSession,
}, info) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const f = await fixture(page, ownerSession.csrf, info.project.name);
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  const create = page.getByRole("button", { name: "提交设计", exact: true });
  const dialog = page.getByRole("dialog");
  await create.focus();
  await page.keyboard.press("Enter");
  await expect(dialog.getByText(/需求 v1/)).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(create).toBeFocused();
  await create.click();
  await fill(page, "预约交互方案");
  await dialog.getByLabel("客户原型.html", { exact: true }).check();
  await dialog
    .getByRole("button", { name: "保存设计提案", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  const section = page.getByRole("region", { name: "设计与原型", exact: true });
  await expect(section.getByText("待方案确认", { exact: true })).toBeVisible();
  const popupPromise = page.waitForEvent("popup");
  await section
    .getByRole("link", { name: "预览 客户原型.html", exact: true })
    .click();
  const popup = await popupPromise;
  const prototype = await prototypeDocument(popup);
  await expect(
    prototype.getByText("网络已阻止", { exact: true }),
  ).toBeVisible();
  await prototype
    .getByRole("button", { name: "选择预约", exact: true })
    .click();
  await expect(
    prototype.getByRole("button", { name: "预约已选择", exact: true }),
  ).toBeVisible();
  await popup.close();
  const workspace = async () =>
    (
      await page.request.get("/api/v1/projects/" + f.project.id + "/workspace")
    ).json();
  const design = (await workspace()).find(
    (x: { kind: string }) => x.kind === "design",
  );
  await page
    .getByLabel("规划采用的设计", { exact: true })
    .selectOption(design.id);
  await page.getByRole("button", { name: "生成方案", exact: true }).click();
  await expect(dialog.getByText(/预约交互方案/)).toBeVisible();
  await dialog
    .getByRole("button", { name: "生成交付方案", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  expect(
    (await workspace()).filter((x: { kind: string }) => x.kind === "task"),
  ).toHaveLength(0);
  const proposed = page.getByRole("article", {
    name: "交付方案 v1",
    exact: true,
  });
  await expect(
    proposed.getByText("先展示预约状态；保留明确的取消确认。", { exact: true }),
  ).toBeVisible();
  await proposed.getByRole("button", { name: "批准方案", exact: true }).click();
  await expect(
    dialog.getByText("先展示预约状态；保留明确的取消确认。", { exact: true }),
  ).toBeVisible();
  await dialog.getByRole("button", { name: "确认批准", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const items = await workspace();
  const plan = items.find((x: { kind: string }) => x.kind === "plan");
  const approval = items.find(
    (x: { kind: string; data: { type: string } }) =>
      x.kind === "approval" && x.data.type === "plan",
  );
  expect(plan.data.design.id).toBe(design.id);
  expect(approval.data.design).toEqual(plan.data.design);
  expect(items.filter((x: { kind: string }) => x.kind === "task")).toHaveLength(
    1,
  );
  await page.getByRole("tab", { name: "交付方案", exact: true }).click();
  await expect(
    section.getByText("当前批准设计", { exact: true }),
  ).toBeVisible();
  const shot = process.env.IS_PLAN_SCREENSHOTS;
  if (shot)
    await page.screenshot({
      path: shot + "/design-approved.png",
      fullPage: true,
      animations: "disabled",
    });
  expect(errors).toEqual([]);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
});

test("lost response retries one proposal and stale dialogs retain the original baseline", async ({
  page,
  ownerSession,
}, info) => {
  const f = await fixture(page, ownerSession.csrf, "恢复 " + info.project.name);
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  const requests: { key: string; expectedRequirementRevision: number }[] = [];
  let lost = true;
  await page.route("**/commands/designs.propose", async (route) => {
    requests.push(route.request().postDataJSON());
    const response = await route.fetch();
    if (lost) {
      lost = false;
      await route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({
          error: { message: "设计已保存，响应暂不可用" },
        }),
      });
    } else await route.fulfill({ response });
  });
  const dialog = page.getByRole("dialog");
  await page.getByRole("button", { name: "提交设计", exact: true }).click();
  await fill(page, "可恢复的设计");
  const save = dialog.getByRole("button", {
    name: "保存设计提案",
    exact: true,
  });
  await save.click();
  await expect(dialog.getByRole("alert")).toContainText("响应暂不可用");
  await save.click();
  await expect(dialog).not.toBeVisible();
  expect(requests).toHaveLength(2);
  expect(requests[0].key).toBe(requests[1].key);
  const workspace = async () =>
    (
      await page.request.get("/api/v1/projects/" + f.project.id + "/workspace")
    ).json();
  const designs = (await workspace()).filter(
    (x: { kind: string }) => x.kind === "design",
  );
  expect(designs).toHaveLength(1);
  await page
    .getByLabel("规划采用的设计", { exact: true })
    .selectOption(designs[0].id);
  await page.getByRole("button", { name: "人工编制", exact: true }).click();
  await dialog
    .getByLabel("方案与交付路径", { exact: true })
    .fill("实现已选择的预约设计并保留独立质量验证。");
  await dialog
    .getByRole("button", { name: "提交方案审阅", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  expect(
    (await workspace()).find((x: { kind: string }) => x.kind === "plan").data
      .design.id,
  ).toBe(designs[0].id);
  await page.getByRole("button", { name: "提交设计", exact: true }).click();
  await fill(page, "过期窗口");
  await f.command("goals.revise", {
    id: f.goal.id,
    reason: "设计期间业务范围变更",
  });
  await save.click();
  await expect(dialog.getByRole("alert")).toContainText("需求版本");
  await save.click();
  await expect(dialog.getByRole("alert")).toContainText("需求版本");
  expect(requests[2].expectedRequirementRevision).toBe(1);
  expect(requests[2].key).toBe(requests[3].key);
  await page.keyboard.press("Escape");
  await expect(
    page.getByRole("button", { name: "提交设计", exact: true }),
  ).toBeDisabled();
  expect(
    (await workspace()).filter((x: { kind: string }) => x.kind === "design"),
  ).toHaveLength(1);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
});

test("a project viewer can inspect designs but cannot submit or activate them", async ({
  page,
  ownerSession,
  playwright,
  browser,
}, info) => {
  const f = await fixture(page, ownerSession.csrf, "只读 " + info.project.name);
  const design = await f.command("designs.propose", {
    goalId: f.goal.id,
    expectedRequirementRevision: 1,
    key: "viewer-record",
    title: "可查看的预约设计",
    summary: "客户查看预约状态",
    decisions: "先展示状态，明确取消确认",
    criteria: ["own"],
    prototypeArtifactIds: [f.prototype.id],
    reason: "共享设计上下文",
  });
  const email = `design-viewer-${info.project.name}-${Date.now()}@example.test`;
  const created = await page.request.post("/api/v1/identities", {
    data: {
      email,
      name: "设计只读成员",
      password: "design-viewer-browser-fixture-2026",
    },
    headers: { Origin: origin, "X-CSRF-Token": ownerSession.csrf },
  });
  expect(created.ok(), await created.text()).toBeTruthy();
  const identity = await created.json();
  await f.command("members.grant", {
    projectId: f.project.id,
    identityId: identity.id,
    roles: ["viewer"],
  });
  const client = await playwright.request.newContext({ baseURL: origin });
  const login = await client.post("/api/v1/auth/login", {
    data: { email, password: "design-viewer-browser-fixture-2026" },
    headers: { Origin: origin },
  });
  expect(login.ok(), await login.text()).toBeTruthy();
  const session = await login.json();
  const mobile = info.project.name === "mobile";
  const context = await browser.newContext({
    storageState: await client.storageState(),
    viewport: mobile
      ? { width: 390, height: 844 }
      : { width: 1440, height: 1000 },
    isMobile: mobile,
    hasTouch: mobile,
  });
  try {
    const viewer = await context.newPage();
    await open(viewer, f.project.id, f.goal.id, mobile);
    await expect(
      viewer.getByRole("button", { name: "提交设计", exact: true }),
    ).toBeDisabled();
    await expect(
      viewer.getByRole("button", { name: "人工编制", exact: true }),
    ).toBeDisabled();
    await expect(
      viewer.getByRole("button", { name: "生成方案", exact: true }),
    ).toBeDisabled();
    await expect(
      viewer.getByRole("heading", {
        name: "设计 v1 · 可查看的预约设计",
        exact: true,
      }),
    ).toBeVisible();
    const denied = await client.post("/api/v1/commands/designs.propose", {
      data: { goalId: f.goal.id },
      headers: { Origin: origin, "X-CSRF-Token": session.csrf },
    });
    expect(denied.status()).toBe(403);
    expect(
      (await client.get("/api/v1/resources/" + design.id)).ok(),
    ).toBeTruthy();
    expect(
      await viewer.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBeTruthy();
  } finally {
    await context.close();
    await client.dispose();
  }
});

test("prototype navigation cannot send requests or replace the authenticated workspace", async ({
  page,
  ownerSession,
  context,
}, info) => {
  const f = await fixture(page, ownerSession.csrf, "网络 " + info.project.name);
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  const workspaceURL = page.url();
  const previewURL =
    origin + "/api/v1/artifacts/" + f.prototype.id + "/preview";
  let navigationRequests = 0;
  await context.route("**/blocked-preview-navigation", async (route) => {
    navigationRequests++;
    await route.fulfill({
      status: 200,
      contentType: "text/plain",
      body: "Local probe only",
    });
  });
  const popup = await context.newPage();
  try {
    await popup.goto(previewURL);
    const prototype = await prototypeDocument(popup);
    await prototype
      .getByRole("button", { name: "尝试页面导航", exact: true })
      .click();
    expect(navigationRequests).toBe(0);
    expect(popup.url()).toBe(previewURL);
    expect(page.url()).toBe(workspaceURL);
    await expect(
      page.getByRole("button", { name: "提交设计", exact: true }),
    ).toBeEnabled();
  } finally {
    await popup.close();
  }
});
