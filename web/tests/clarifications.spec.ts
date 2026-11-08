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
async function goalPage(
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
  await page.getByRole("tab", { name: "需求基线", exact: true }).click();
}

test("business questions require human answers, freeze at approval and retain earlier scope", async ({
  page,
  ownerSession,
}, info) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const command = (name: string, input: unknown) =>
    post(page.request, "commands/" + name, input, ownerSession.csrf);
  const project = await command("projects.create", {
    title: "需求澄清 " + info.project.name,
  });
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "客户预约应用",
    outcome: "客户预约服务并在规则允许时取消。",
    criteria: [{ id: "booking", text: "预约和取消符合已确认权限规则" }],
    targets: ["web"],
    budget: 10,
  });
  await goalPage(page, project.id, goal.id, info.project.name === "mobile");
  const ask = page.getByRole("button", { name: "提出澄清", exact: true });
  await expect(ask).toBeVisible({ timeout: 2000 });
  await ask.focus();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(ask).toBeFocused();
  await ask.click();
  await dialog.getByRole("button", { name: "提交问题", exact: true }).click();
  await expect(dialog).toBeVisible();
  await dialog
    .getByLabel("需要确认的问题", { exact: true })
    .fill("谁可以取消已确认预约？");
  await dialog
    .getByLabel("业务影响", { exact: true })
    .fill("需要明确客户与员工的权限边界。");
  await dialog.getByRole("button", { name: "提交问题", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const approve = page.getByRole("button", { name: "审阅并确认", exact: true });
  await expect(approve).toBeDisabled();
  const blocked = await page.request.post("/api/v1/commands/goals.approve", {
    data: { id: goal.id, expectedRequirementRevision: 1 },
    headers: { Origin: origin, "X-CSRF-Token": ownerSession.csrf },
  });
  expect(blocked.status()).toBe(409);
  const workspaceURL = "/api/v1/projects/" + project.id + "/workspace";
  const questions = async () => {
    const state = await (await page.request.get(workspaceURL)).json();
    return state.filter(
      (item: { kind: string }) => item.kind === "clarification",
    );
  };
  const first = (await questions())[0].data.questions[0];
  const row = page.getByRole("article", {
    name: "澄清问题 " + first.id,
    exact: true,
  });
  await row.getByRole("button", { name: "确认答复", exact: true }).click();
  await dialog
    .getByRole("button", { name: "保存确认答复", exact: true })
    .click();
  await expect(dialog).toBeVisible();
  await dialog
    .getByLabel("产品确认答复", { exact: true })
    .fill("客户只能取消本人的预约；员工可取消任何预约并说明原因。");
  await dialog
    .getByRole("button", { name: "保存确认答复", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await expect(
    row.getByText("客户只能取消本人的预约；员工可取消任何预约并说明原因。", {
      exact: true,
    }),
  ).toBeVisible();
  await expect(approve).toBeEnabled();
  await page.getByRole("button", { name: "辅助生成问题", exact: true }).click();
  const generationSent = page.waitForRequest((request) =>
    request.url().endsWith("/api/v1/clarifications/generate"),
  );
  await dialog
    .getByRole("button", { name: "生成澄清问题", exact: true })
    .click();
  const generationInput = (await generationSent).postDataJSON();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByText("预约取消后，已支付款项如何处理？", { exact: true }),
  ).toBeVisible();
  await expect(approve).toBeDisabled();
  // A lost HTTP reply is replayed from the durable result, without a model call.
  const replay = await post(
    page.request,
    "clarifications/generate",
    generationInput,
    ownerSession.csrf,
  );
  const generated = (await questions()).find(
    (batch: { data: { origin: string } }) => batch.data.origin === "model",
  );
  expect(replay.id).toBe(generated.id);
  const second = generated.data.questions[0];
  await page
    .getByRole("article", { name: "澄清问题 " + second.id, exact: true })
    .getByRole("button", { name: "确认答复", exact: true })
    .click();
  await dialog
    .getByLabel("产品确认答复", { exact: true })
    .fill("首版只记录线下付款，取消时由员工线下处理退款并记录结果。");
  await dialog
    .getByRole("button", { name: "保存确认答复", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await approve.click();
  await dialog.getByRole("button", { name: "确认批准", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(ask).toBeDisabled();
  await command("goals.revise", {
    id: goal.id,
    reason: "下一版确认取消后的通知方式",
  });
  await goalPage(page, project.id, goal.id, info.project.name === "mobile");
  await page.getByText("历史澄清（2）", { exact: true }).click();
  await expect(
    row.getByText("客户只能取消本人的预约；员工可取消任何预约并说明原因。", {
      exact: true,
    }),
  ).toBeVisible();
  await ask.click();
  await dialog
    .getByLabel("需要确认的问题", { exact: true })
    .fill("取消预约后，应通知哪些人员？");
  await dialog.getByRole("button", { name: "提交问题", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(approve).toBeDisabled();
  const current = (await questions()).find(
    (batch: { data: { requirementRevision: number } }) =>
      batch.data.requirementRevision === 2,
  );
  const other = await command("goals.create", {
    projectId: project.id,
    title: "另一个需求范围",
    outcome: "只检查本目标的需求决定。",
    criteria: [{ id: "scope", text: "不混入其他目标的问答" }],
    targets: ["web"],
    budget: 10,
  });
  await goalPage(page, project.id, other.id, info.project.name === "mobile");
  await expect(
    page.getByText("取消预约后，应通知哪些人员？", { exact: true }),
  ).toHaveCount(0);
  await ask.click();
  await expect(
    dialog.getByText("另一个需求范围 · 需求 v1", { exact: true }),
  ).toBeVisible();
  await command("goals.revise", {
    id: other.id,
    reason: "打开提问框期间业务范围变化",
  });
  await dialog
    .getByLabel("需要确认的问题", { exact: true })
    .fill("旧范围的回复能否写入新版本？");
  await dialog.getByRole("button", { name: "提交问题", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("需求版本已变化");
  await page.keyboard.press("Escape");
  await goalPage(page, project.id, goal.id, info.project.name === "mobile");
  await expect(
    page.getByText("取消预约后，应通知哪些人员？", { exact: true }),
  ).toBeVisible();
  const identity = await post(
    page.request,
    "identities",
    {
      email: "requirements-observer@example.test",
      name: "需求观察员",
      password: "requirements-observer-password-2026",
    },
    ownerSession.csrf,
  );
  await command("members.grant", {
    projectId: project.id,
    identityId: identity.id,
    roles: ["viewer"],
  });
  const readonlyContext = await page
    .context()
    .browser()!
    .newContext({
      baseURL: origin,
      viewport: {
        width: info.project.name === "mobile" ? 390 : 1440,
        height: info.project.name === "mobile" ? 844 : 1000,
      },
      isMobile: info.project.name === "mobile",
      hasTouch: info.project.name === "mobile",
    });
  try {
    const session = await post(
      readonlyContext.request,
      "auth/login",
      {
        email: "requirements-observer@example.test",
        password: "requirements-observer-password-2026",
      },
      "",
    );
    const readonly = await readonlyContext.newPage();
    readonly.on("pageerror", (error) => errors.push(error.message));
    await goalPage(
      readonly,
      project.id,
      goal.id,
      info.project.name === "mobile",
    );
    await expect(
      readonly.getByRole("button", { name: "提出澄清", exact: true }),
    ).toBeDisabled();
    await expect(
      readonly.getByRole("button", { name: "确认答复", exact: true }),
    ).toBeDisabled();
    const denied = await readonlyContext.request.post(
      "/api/v1/commands/clarifications.answer",
      {
        data: {
          id: current.id,
          questionId: current.data.questions[0].id,
          expectedRequirementRevision: 2,
          key: "viewer",
          answer: "Unauthorized business decision",
        },
        headers: { Origin: origin, "X-CSRF-Token": session.csrf },
      },
    );
    expect(denied.status()).toBe(403);
  } finally {
    await readonlyContext.close();
  }
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
  expect(errors).toEqual([]);
  await page.screenshot({
    path:
      process.env.IS_CLARIFICATION_SCREENSHOTS +
      "/" +
      info.project.name +
      "-requirement-clarification.png",
    fullPage: true,
    animations: "disabled",
  });
});

test("failed discovery keeps its charge and a repeated confirmation does not resend the model", async ({
  page,
  ownerSession,
}, info) => {
  const command = (name: string, input: unknown) =>
    post(page.request, "commands/" + name, input, ownerSession.csrf);
  const project = await command("projects.create", { title: "生成失败核对" });
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "待澄清目标",
    outcome: "local-fixture-failure: 需要人工核对模型失败费用。",
    criteria: [{ id: "retry", text: "已发送的生成不自动重发" }],
    targets: ["web"],
    budget: 10,
  });
  await goalPage(page, project.id, goal.id, info.project.name === "mobile");
  await expect(
    page.getByRole("button", { name: "辅助生成问题", exact: true }),
  ).toBeVisible({ timeout: 2000 });
  await page.getByRole("button", { name: "辅助生成问题", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog
    .getByRole("button", { name: "生成澄清问题", exact: true })
    .click();
  await expect(dialog.getByRole("alert")).toContainText("已保留费用");
  await dialog
    .getByRole("button", { name: "生成澄清问题", exact: true })
    .click();
  await expect(dialog.getByRole("alert")).toContainText("未重复调用模型");
  await page.keyboard.press("Escape");
  await page.getByText("辅助生成记录（1）", { exact: true }).click();
  await expect(page.getByText("生成未完成", { exact: true })).toBeVisible();
  const costs = await (
    await page.request.get(
      "/api/v1/projects/" + project.id + "/costs?goalId=" + goal.id,
    )
  ).json();
  expect(costs.modelPendingCents).toBeGreaterThan(0);
  expect(costs.charges).toHaveLength(1);
  const ordinary = await page.request.post("/api/v1/model/chat", {
    data: {
      goalId: goal.id,
      allowDraft: true,
      messages: [{ role: "user", content: "Cannot bypass approval" }],
    },
    headers: { Origin: origin, "X-CSRF-Token": ownerSession.csrf },
  });
  expect(ordinary.status()).toBe(409);
});
