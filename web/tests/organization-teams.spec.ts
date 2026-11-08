import { test, expect } from "./fixtures";
import type { APIRequestContext, Page } from "@playwright/test";

const origin = "http://127.0.0.1:18082";
async function post(
  client: APIRequestContext,
  path: string,
  data: unknown,
  csrf: string,
) {
  const r = await client.post(origin + "/api/v1/" + path, {
    data,
    headers: { Origin: origin, "X-CSRF-Token": csrf },
  });
  const value = await r.json();
  expect(r.ok(), JSON.stringify(value)).toBeTruthy();
  return value;
}
async function governance(
  page: Page,
  project: string,
  mobile: boolean,
  tab = "组织团队",
) {
  await page.goto("/");
  if (mobile)
    await page.getByRole("button", { name: "打开导航", exact: true }).click();
  await page.getByLabel("选择项目").selectOption(project);
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("button", { name: "企业治理", exact: true })
    .click();
  await expect(page.getByRole("tab", { name: tab, exact: true })).toBeVisible({
    timeout: 3000,
  });
  await page.getByRole("tab", { name: tab, exact: true }).click();
}

test("enterprise team lifecycle governs inherited owner and direct role union", async ({
  page,
  browser,
  ownerSession,
}, info) => {
  const command = (name: string, input: unknown) =>
    post(page.request, "commands/" + name, input, ownerSession.csrf);
  const project = await command("projects.create", {
    title: "团队交付 " + info.project.name,
  });
  const email = "team-lifecycle-" + info.project.name + "@example.test";
  const user = await post(
    page.request,
    "identities",
    { email, name: "团队负责人", password: "teams-browser-password-2026" },
    ownerSession.csrf,
  );
  const direct = await command("members.grant", {
    projectId: project.id,
    identityId: user.id,
    roles: ["viewer"],
  });
  const mobile = info.project.name === "mobile";
  await governance(page, project.id, mobile);
  await page.getByRole("button", { name: "新建组织团队", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("团队名称", { exact: true }).fill("交付工程组");
  await dialog
    .getByLabel("团队说明", { exact: true })
    .fill("负责新应用研发与正式交付");
  await dialog.getByRole("checkbox", { name: /^团队负责人 </ }).check();
  await dialog.getByRole("button", { name: "创建团队", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const team = (await (await page.request.get("/api/v1/teams")).json()).find(
    (t: any) => t.title === "交付工程组",
  );
  const teamRow = page.getByRole("article", {
    name: "组织团队 " + team.id,
    exact: true,
  });
  await expect(
    teamRow.getByText("1 位启用成员", { exact: true }),
  ).toBeVisible();
  await teamRow
    .getByRole("button", { name: "授权到当前项目", exact: true })
    .click();
  await dialog.getByRole("checkbox", { name: "开发", exact: true }).uncheck();
  await dialog
    .getByRole("checkbox", { name: "项目负责人", exact: true })
    .check();
  await dialog
    .getByLabel("授权原因", { exact: true })
    .fill("团队承担项目交付责任");
  await dialog
    .getByRole("button", { name: "确认团队授权", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  const context = await browser.newContext({
    viewport: page.viewportSize()!,
    isMobile: mobile,
    hasTouch: mobile,
  });
  try {
    const auth = await post(
      context.request,
      "auth/login",
      { email, password: "teams-browser-password-2026" },
      "",
    );
    const token = await post(
      context.request,
      "tokens",
      { projectId: project.id },
      auth.csrf,
    );
    const memberPage = await context.newPage();
    await governance(memberPage, project.id, mobile, "项目成员");
    await expect(
      memberPage.getByRole("tab", { name: "访问凭据", exact: true }),
    ).toBeVisible();
    await expect(
      memberPage.getByText("来自团队：交付工程组", { exact: true }),
    ).toBeVisible();
    await command("members.revoke", {
      id: direct.id,
      expectedRevision: direct.revision,
      reason: "只移除直接授权",
    });
    await expect
      .poll(async () =>
        (
          await context.request.get(
            "/api/v1/projects/" + project.id + "/workspace",
          )
        ).status(),
      )
      .toBe(200);
    await governance(memberPage, project.id, mobile, "项目成员");
    await expect(
      memberPage.getByText("来自团队：交付工程组", { exact: true }),
    ).toBeVisible();
    await expect(
      memberPage.getByRole("button", { name: "添加项目成员", exact: true }),
    ).toBeVisible();
    await teamRow
      .getByRole("button", { name: "停用团队", exact: true })
      .click();
    await dialog.getByLabel("变更原因", { exact: true }).fill("暂停部门授权");
    await dialog
      .getByRole("button", { name: "确认停用团队", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await expect(teamRow.getByText("已停用", { exact: true })).toBeVisible();
    await expect(
      memberPage.getByRole("heading", { name: "等待项目授权", exact: true }),
    ).toBeVisible({ timeout: 12000 });
    expect(
      (
        await context.request.get("/api/v1/projects", {
          headers: { Authorization: "Bearer " + token.token },
        })
      ).status(),
    ).toBe(401);
    await teamRow
      .getByRole("button", { name: "启用团队", exact: true })
      .click();
    await dialog.getByLabel("变更原因", { exact: true }).fill("恢复部门职责");
    await dialog
      .getByRole("button", { name: "确认启用团队", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await governance(memberPage, project.id, mobile);
    await expect(
      memberPage.getByRole("button", { name: "新建组织团队", exact: true }),
    ).toHaveCount(0);
    expect(
      (
        await context.request.get("/api/v1/projects", {
          headers: { Authorization: "Bearer " + token.token },
        })
      ).status(),
    ).toBe(401);
    await expect(
      page.getByRole("article", { name: "项目团队 " + team.id, exact: true }),
    ).toContainText("团队成员变更会动态生效");
    await page.screenshot({
      path: info.outputPath("organization-teams.png"),
      fullPage: true,
    });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBeTruthy();
    expect(
      await memberPage.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBeTruthy();
  } finally {
    await context.close();
  }
});

test("team forms handle stale revisions, catalog failures, loading and keyboard focus", async ({
  page,
  ownerSession,
}, info) => {
  const command = (name: string, input: unknown) =>
    post(page.request, "commands/" + name, input, ownerSession.csrf);
  const project = await command("projects.create", {
    title: "团队冲突 " + info.project.name,
  });
  const team = await command("teams.create", {
    title: "平台研发组",
    memberIds: [],
    key: "browser-team-conflict",
  });
  await governance(page, project.id, info.project.name === "mobile");
  const row = page.getByRole("article", {
    name: "组织团队 " + team.id,
    exact: true,
  });
  const edit = row.getByRole("button", { name: "编辑团队", exact: true });
  await edit.click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(dialog).not.toBeVisible();
  await expect(edit).toBeFocused();
  await edit.click();
  await expect(dialog).toBeVisible();
  await command("teams.update", {
    id: team.id,
    expectedRevision: 1,
    title: "平台研发组",
    description: "已被另一位管理员更新",
    memberIds: [],
    reason: "并发修改",
    key: "concurrent",
  });
  await dialog.getByLabel("变更原因", { exact: true }).fill("旧版本编辑");
  await dialog.getByLabel("团队名称", { exact: true }).fill("过期修改");
  await dialog.getByRole("button", { name: "保存团队", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("版本");
  await expect(dialog.getByLabel("团队名称", { exact: true })).toHaveValue(
    "过期修改",
  );
  await page.keyboard.press("Escape");
  await page.route("**/api/v1/teams", (route) =>
    route.fulfill({
      status: 503,
      contentType: "application/json",
      body: JSON.stringify({ error: { message: "目录暂时不可用" } }),
    }),
  );
  await page.getByRole("button", { name: "刷新团队目录", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("目录暂时不可用");
  await page.unroute("**/api/v1/teams");
  let release!: () => void;
  const hold = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/api/v1/teams", async (route) => {
    await hold;
    await route.continue();
  });
  await page.getByRole("button", { name: "刷新团队目录", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("正在加载团队目录");
  release();
  await expect(page.getByRole("status")).not.toBeVisible();
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(row).toContainText("平台研发组");
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
});

test("inherited developer sees effective sources and cannot grant authority", async ({
  page,
  browser,
  ownerSession,
}, info) => {
  const command = (name: string, input: unknown) =>
    post(page.request, "commands/" + name, input, ownerSession.csrf);
  const project = await command("projects.create", {
    title: "开发团队 " + info.project.name,
  });
  const email = "team-developer-" + info.project.name + "@example.test";
  const user = await post(
    page.request,
    "identities",
    { email, name: "团队开发成员", password: "teams-browser-password-2026" },
    ownerSession.csrf,
  );
  const team = await command("teams.create", {
    title: "应用开发组",
    memberIds: [user.id],
    key: "developer-team",
  });
  const grant = await command("teams.grant", {
    projectId: project.id,
    teamId: team.id,
    expectedTeamRevision: team.revision,
    roles: ["developer"],
    reason: "实现交付范围",
    key: "developer-grant",
  });
  const mobile = info.project.name === "mobile";
  const context = await browser.newContext({
    viewport: page.viewportSize()!,
    isMobile: mobile,
    hasTouch: mobile,
  });
  try {
    await post(
      context.request,
      "auth/login",
      { email, password: "teams-browser-password-2026" },
      "",
    );
    const memberPage = await context.newPage();
    await governance(memberPage, project.id, mobile, "项目成员");
    await expect(
      memberPage.getByText("当前有效角色：开发", { exact: true }),
    ).toBeVisible();
    await expect(
      memberPage.getByRole("button", { name: "添加项目成员", exact: true }),
    ).toHaveCount(0);
    await expect(
      memberPage.getByRole("tab", { name: "访问凭据", exact: true }),
    ).toHaveCount(0);
    await memberPage
      .getByRole("tab", { name: "组织团队", exact: true })
      .click();
    await expect(
      memberPage.getByRole("article", {
        name: "项目团队 " + team.id,
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      memberPage.getByRole("button", { name: "撤销团队授权", exact: true }),
    ).toHaveCount(0);
    await governance(page, project.id, mobile);
    const binding = page.getByRole("article", {
      name: "项目团队 " + team.id,
      exact: true,
    });
    await binding
      .getByRole("button", { name: "撤销团队授权", exact: true })
      .click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("授权原因", { exact: true }).fill("团队完成工作");
    await dialog
      .getByRole("button", { name: "确认撤销授权", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await expect(binding.getByText("已移除", { exact: true })).toBeVisible();
    await expect(
      memberPage.getByRole("heading", { name: "等待项目授权", exact: true }),
    ).toBeVisible({ timeout: 12000 });
    const stored = await (
      await page.request.get("/api/v1/resources/" + grant.id)
    ).json();
    expect(stored.revision).toBe(2);
    expect(stored.status).toBe("revoked");
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBeTruthy();
  } finally {
    await context.close();
  }
});
