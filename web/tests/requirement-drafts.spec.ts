import { test, expect } from "./fixtures";
import type { Page } from "@playwright/test";

const origin = "http://127.0.0.1:18082";
async function delivery(page: Page, project: string, mobile: boolean) {
  await page.goto("/");
  if (mobile)
    await page.getByRole("button", { name: "打开导航", exact: true }).click();
  await page.getByLabel("选择项目").selectOption(project);
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("button", { name: /^目标与需求/ })
    .click();
}

test("business description becomes human-reviewed requirements and a separately approved baseline", async ({
  page,
  ownerSession,
}, info) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const command = async (name: string, data: unknown) => {
    const r = await page.request.post("/api/v1/commands/" + name, {
      data,
      headers: { Origin: origin, "X-CSRF-Token": ownerSession.csrf },
    });
    expect(r.ok(), await r.text()).toBeTruthy();
    return r.json();
  };
  const project = await command("projects.create", {
    title: "需求整理 " + info.project.name,
  });
  await delivery(page, project.id, info.project.name === "mobile");
  await page.getByRole("button", { name: "新建交付目标", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog
    .getByText("交付目标", { exact: true })
    .locator("..")
    .locator("input")
    .fill("客户预约服务");
  await dialog
    .getByText("业务结果", { exact: true })
    .locator("..")
    .locator("textarea")
    .fill("客户预约服务并取消自己的预约。首版不接在线支付。");
  await dialog
    .getByRole("button", { name: "创建交付目标", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  const approve = page.getByRole("button", { name: "审阅并确认", exact: true });
  await expect(approve).toBeDisabled();
  await expect(
    page.getByText("请先整理至少一项验收标准", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "提出澄清", exact: true }).click();
  await dialog
    .getByLabel("需要确认的问题", { exact: true })
    .fill("谁可以取消预约？");
  await dialog.getByRole("button", { name: "提交问题", exact: true }).click();
  const generate = page.getByRole("button", {
    name: "辅助整理需求",
    exact: true,
  });
  await expect(generate).toBeDisabled();
  await page.getByRole("button", { name: "确认答复", exact: true }).click();
  await dialog
    .getByLabel("产品确认答复", { exact: true })
    .fill("客户只能取消自己的预约，首版不接在线支付。");
  await dialog
    .getByRole("button", { name: "保存确认答复", exact: true })
    .click();
  await expect(generate).toBeEnabled();
  await generate.focus();
  await page.keyboard.press("Enter");
  await expect(dialog).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(generate).toBeFocused();
  await generate.click();
  await expect(dialog.getByText(/计入目标模型预算/)).toBeVisible();
  const request = page.waitForRequest((r) =>
    r.url().endsWith("/api/v1/requirements/generate"),
  );
  await dialog.getByRole("button", { name: "生成需求稿", exact: true }).click();
  const input = (await request).postDataJSON();
  await expect(dialog).not.toBeVisible();
  const draft = page.getByRole("article", { name: "需求稿", exact: true });
  await expect(draft).toBeVisible();
  await expect(draft.getByText("模型整理", { exact: true })).toBeVisible();
  await expect(approve).toBeDisabled();
  const replay = await page.request.post("/api/v1/requirements/generate", {
    data: input,
    headers: { Origin: origin, "X-CSRF-Token": ownerSession.csrf },
  });
  expect(replay.ok()).toBeTruthy();
  await draft.getByRole("button", { name: "审阅并采纳", exact: true }).click();
  await expect(
    dialog.getByRole("heading", { name: "当前验收标准", exact: true }),
  ).toBeVisible();
  await dialog
    .getByLabel("需求说明", { exact: true })
    .fill("人工审阅：客户预约与本人取消；员工管理时段。首版排除在线支付。");
  await dialog
    .getByLabel("验收标准 1", { exact: true })
    .fill("登录客户无法取消其他客户的预约。");
  await dialog
    .getByLabel("采纳原因", { exact: true })
    .fill("已核对客户权限与排除范围，并修订验收表述。");
  await dialog
    .getByRole("button", { name: "采纳并创建新版本", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByText(
      "人工审阅：客户预约与本人取消；员工管理时段。首版排除在线支付。",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(approve).toBeEnabled();
  const workspace = async () =>
    (
      await page.request.get("/api/v1/projects/" + project.id + "/workspace")
    ).json();
  const resources = await workspace();
  const goal = resources.find((x: { kind: string }) => x.kind === "goal");
  expect(goal.revision).toBe(2);
  expect(goal.data.approvedRevision).toBe(0);
  const decisions = resources.filter(
    (x: { kind: string; data: { requirementRevision: number } }) =>
      x.kind === "clarification" && x.data.requirementRevision === 2,
  );
  expect(decisions).toHaveLength(1);
  expect(decisions[0].data.questions[0].answeredBy).toBeTruthy();
  await approve.click();
  await dialog.getByRole("button", { name: "确认批准", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const approved = (await workspace()).find(
    (x: { kind: string }) => x.kind === "goal",
  );
  expect(approved.data.approvedRevision).toBe(2);
  const screenshot = process.env.IS_REQUIREMENT_SCREENSHOTS;
  if (screenshot)
    await page.screenshot({
      path: screenshot + "/approved.png",
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

test("failed draft generation retains a visible record and manual review remains available", async ({
  page,
  ownerSession,
}, info) => {
  const command = async (name: string, data: unknown) => {
    const r = await page.request.post("/api/v1/commands/" + name, {
      data,
      headers: { Origin: origin, "X-CSRF-Token": ownerSession.csrf },
    });
    expect(r.ok(), await r.text()).toBeTruthy();
    return r.json();
  };
  const project = await command("projects.create", {
    title: "需求失败处理 " + info.project.name,
  });
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "人工确认需求",
    outcome: "local-fixture-failure 预约服务",
    targets: ["web"],
    budget: 10,
  });
  await delivery(page, project.id, info.project.name === "mobile");
  await page.getByLabel("选择交付目标").selectOption(goal.id);
  await page.getByRole("button", { name: "辅助整理需求", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "生成需求稿", exact: true }).click();
  await expect(dialog.getByRole("alert")).toBeVisible();
  await page.keyboard.press("Escape");
  await page.getByText("需求整理记录（1）", { exact: true }).click();
  await expect(page.getByText("生成未完成", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "人工起草需求", exact: true }).click();
  await dialog
    .getByLabel("需求说明", { exact: true })
    .fill("客户管理自己的预约；首版无支付。");
  await dialog
    .getByLabel("验收标准 1", { exact: true })
    .fill("客户只能访问本人预约。");
  await dialog
    .getByRole("button", { name: "保存待审阅需求稿", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  const draft = page.getByRole("article", { name: "需求稿", exact: true });
  await draft.getByRole("button", { name: "审阅并采纳", exact: true }).click();
  await dialog.getByLabel("采纳原因", { exact: true }).fill("人工核对后采纳");
  // Change the captured context while a reviewer holds the dialog open.
  await command("knowledge.create", {
    projectId: project.id,
    title: "新增约束",
    content: "新增取消时限",
    source: "业务确认",
  });
  await dialog
    .getByRole("button", { name: "采纳并创建新版本", exact: true })
    .click();
  await expect(dialog.getByRole("alert")).toContainText("上下文");
  await page.keyboard.press("Escape");
  const resources = await (
    await page.request.get("/api/v1/projects/" + project.id + "/workspace")
  ).json();
  expect(
    resources.find((x: { kind: string }) => x.kind === "goal").revision,
  ).toBe(1);
});
