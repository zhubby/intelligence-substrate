import { test, expect } from "./fixtures";
import type { APIRequestContext, Page } from "@playwright/test";

const origin = "http://127.0.0.1:18082";
async function post(
  client: APIRequestContext,
  path: string,
  data: unknown,
  csrf: string,
) {
  const response = await client.post(origin + "/api/v1/" + path, {
    data,
    headers: { Origin: origin, "X-CSRF-Token": csrf },
  });
  const value = await response.json();
  expect(response.ok(), JSON.stringify(value)).toBeTruthy();
  return value;
}
async function membersPage(page: Page, project: string, mobile: boolean) {
  await page.goto("/");
  if (mobile)
    await page.getByRole("button", { name: "打开导航", exact: true }).click();
  await page.getByLabel("选择项目").selectOption(project);
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("button", { name: "企业治理", exact: true })
    .click();
  await page.getByRole("tab", { name: "项目成员", exact: true }).click();
}

test("membership role edit, removal and regrant invalidate old delegation", async ({
  page,
  playwright,
  ownerSession,
}, info) => {
  const command = (name: string, input: unknown) =>
    post(page.request, "commands/" + name, input, ownerSession.csrf);
  const project = await command("projects.create", {
    title: "Member lifecycle " + info.project.name + " " + Date.now(),
  });
  const email =
    "member-" + info.project.name + "-" + Date.now() + "@example.test";
  const user = await post(
    page.request,
    "identities",
    {
      email,
      name: "交付项目负责人",
      password: "membership-browser-password-2026",
    },
    ownerSession.csrf,
  );
  await command("members.grant", {
    projectId: project.id,
    identityId: user.id,
    roles: ["owner", "quality"],
  });
  const member = await playwright.request.newContext({ baseURL: origin });
  try {
    const login = await post(
      member,
      "auth/login",
      { email, password: "membership-browser-password-2026" },
      "",
    );
    const token = await post(
      member,
      "tokens",
      { projectId: project.id },
      login.csrf,
    );
    await membersPage(page, project.id, info.project.name === "mobile");
    const row = page.getByRole("article", {
      name: "成员 " + user.id,
      exact: true,
    });
    await row.getByRole("button", { name: "编辑角色", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await expect(
      dialog.getByRole("checkbox", { name: "项目负责人", exact: true }),
    ).toBeChecked();
    await expect(
      dialog.getByRole("checkbox", { name: "质量", exact: true }),
    ).toBeChecked();
    await dialog.getByRole("checkbox", { name: "发布", exact: true }).check();
    await dialog.getByRole("button", { name: "保存角色", exact: true }).click();
    await expect(dialog).not.toBeVisible();
    await expect(
      row.getByText("项目负责人 · 质量 · 发布", { exact: true }),
    ).toBeVisible();
    expect(
      (
        await member.get("/api/v1/projects", {
          headers: { Authorization: "Bearer " + token.token },
        })
      ).status(),
    ).toBe(401);
    const nextToken = await post(
      member,
      "tokens",
      { projectId: project.id },
      login.csrf,
    );
    await row.getByRole("button", { name: "移除成员", exact: true }).click();
    await expect(
      dialog.getByText(
        "该成员在此项目签发的 Agent 和 Worker 凭据会立即失效。",
        { exact: true },
      ),
    ).toBeVisible();
    await dialog.getByRole("button", { name: "确认移除", exact: true }).click();
    await expect(dialog).toBeVisible();
    await dialog
      .getByLabel("移除原因", { exact: true })
      .fill("职责调整，保留审计历史");
    await dialog.getByRole("button", { name: "确认移除", exact: true }).click();
    await expect(dialog).not.toBeVisible();
    await expect(row.getByText("已移除", { exact: true })).toBeVisible();
    await expect(row.getByText(/职责调整，保留审计历史/)).toBeVisible();
    expect(
      (
        await member.get("/api/v1/projects/" + project.id + "/workspace")
      ).status(),
    ).toBe(403);
    expect(
      (
        await member.get("/api/v1/projects", {
          headers: { Authorization: "Bearer " + nextToken.token },
        })
      ).status(),
    ).toBe(401);
    await row.getByRole("button", { name: "重新授权", exact: true }).click();
    await dialog
      .getByRole("button", { name: "确认重新授权", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await expect(row.getByText("已启用", { exact: true })).toBeVisible();
    expect(
      (
        await member.get("/api/v1/projects/" + project.id + "/workspace")
      ).status(),
    ).toBe(200);
    expect(
      (
        await member.get("/api/v1/projects", {
          headers: { Authorization: "Bearer " + nextToken.token },
        })
      ).status(),
    ).toBe(401);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBeTruthy();
    await page.screenshot({
      path: info.outputPath("member-lifecycle.png"),
      fullPage: true,
    });
  } finally {
    await member.dispose();
  }
});

test("member conflicts and last owner protection stay local and Escape returns focus", async ({
  page,
  ownerSession,
}, info) => {
  const command = (name: string, input: unknown) =>
    post(page.request, "commands/" + name, input, ownerSession.csrf);
  const project = await command("projects.create", {
    title: "Member conflict " + info.project.name + " " + Date.now(),
  });
  await membersPage(page, project.id, info.project.name === "mobile");
  const ownerRow = page
    .getByRole("article")
    .filter({
      has: page.getByRole("button", { name: "编辑角色", exact: true }),
    })
    .first();
  const edit = ownerRow.getByRole("button", { name: "编辑角色", exact: true });
  await edit.click();
  const dialog = page.getByRole("dialog");
  await dialog
    .getByRole("checkbox", { name: "项目负责人", exact: true })
    .uncheck();
  await dialog.getByRole("checkbox", { name: "只读", exact: true }).check();
  await dialog.getByRole("button", { name: "保存角色", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("至少一位");
  await page.keyboard.press("Escape");
  await expect(dialog).not.toBeVisible();
  await expect(edit).toBeFocused();
  const user = await post(
    page.request,
    "identities",
    {
      email: "stale-" + info.project.name + "-" + Date.now() + "@example.test",
      name: "版本冲突成员",
      password: "membership-browser-password-2026",
    },
    ownerSession.csrf,
  );
  await command("members.grant", {
    projectId: project.id,
    identityId: user.id,
    roles: ["developer"],
  });
  await page.getByRole("button", { name: "刷新工作空间", exact: true }).click();
  const row = page.getByRole("article", {
    name: "成员 " + user.id,
    exact: true,
  });
  await row.getByRole("button", { name: "编辑角色", exact: true }).click();
  await command("members.grant", {
    projectId: project.id,
    identityId: user.id,
    roles: ["viewer"],
  });
  await dialog.getByRole("button", { name: "保存角色", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("版本已变化");
  await expect(
    dialog.getByRole("checkbox", { name: "开发", exact: true }),
  ).toBeChecked();
  await page.screenshot({
    path: info.outputPath("member-local-error.png"),
    fullPage: true,
  });
  await page.keyboard.press("Escape");
});

test("removing current project access refreshes selection and clears the workspace", async ({
  page,
  playwright,
  ownerSession,
}, info) => {
  const command = (name: string, input: unknown) =>
    post(page.request, "commands/" + name, input, ownerSession.csrf);
  const project = await command("projects.create", {
    title: "Departing owner " + info.project.name + " " + Date.now(),
  });
  const email =
    "depart-" + info.project.name + "-" + Date.now() + "@example.test";
  const user = await post(
    page.request,
    "identities",
    { email, name: "移交负责人", password: "membership-browser-password-2026" },
    ownerSession.csrf,
  );
  const membership = await command("members.grant", {
    projectId: project.id,
    identityId: user.id,
    roles: ["owner"],
  });
  const client = await playwright.request.newContext({ baseURL: origin });
  const login = await post(
    client,
    "auth/login",
    { email, password: "membership-browser-password-2026" },
    "",
  );
  const context = await page
    .context()
    .browser()!
    .newContext({
      storageState: await client.storageState(),
      viewport:
        info.project.name === "mobile"
          ? { width: 390, height: 844 }
          : { width: 1440, height: 1000 },
    });
  const memberPage = await context.newPage();
  try {
    await membersPage(memberPage, project.id, info.project.name === "mobile");
    await memberPage
      .getByRole("article", { name: "成员 " + user.id, exact: true })
      .getByRole("button", { name: "移除成员", exact: true })
      .click();
    const dialog = memberPage.getByRole("dialog");
    await dialog.getByLabel("移除原因", { exact: true }).fill("本人完成交接");
    await dialog.getByRole("button", { name: "确认移除", exact: true }).click();
    await expect(dialog).not.toBeVisible();
    await expect(
      memberPage.getByRole("heading", { name: "等待项目授权", exact: true }),
    ).toBeVisible();
    await expect(
      memberPage.getByRole("article", { name: "成员 " + user.id, exact: true }),
    ).toHaveCount(0);
    expect((await client.get("/api/v1/projects")).status()).toBe(200);
    const snapshot = await page.request.get(
      "/api/v1/resources/" + membership.id,
    );
    expect((await snapshot.json()).status).toBe("revoked");
    expect(
      await memberPage.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBeTruthy();
    await memberPage.screenshot({
      path: info.outputPath("member-access-loss.png"),
      fullPage: true,
    });
  } finally {
    await context.close();
    await client.dispose();
  }
});
