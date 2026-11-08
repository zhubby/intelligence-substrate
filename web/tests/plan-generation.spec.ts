import { test, expect } from "./fixtures";
import type { Page } from "@playwright/test";

const origin = "http://127.0.0.1:18082";
async function openPlans(
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

test("planning is confirmed, replay-safe and separate from human activation", async ({
  page,
  ownerSession,
}, info) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const command = async (name: string, data: unknown) => {
    const response = await page.request.post("/api/v1/commands/" + name, {
      data,
      headers: { Origin: origin, "X-CSRF-Token": ownerSession.csrf },
    });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  const project = await command("projects.create", {
    title: "方案恢复 " + info.project.name,
  });
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "客户预约与取消",
    outcome: "客户只能取消自己的预约，首版不接支付。",
    criteria: [{ id: "own", text: "不能取消他人的预约" }],
    targets: ["web"],
  });
  await command("goals.approve", { id: goal.id });
  await openPlans(page, project.id, goal.id, info.project.name === "mobile");
  const generate = page.getByRole("button", { name: "生成方案", exact: true });
  const dialog = page.getByRole("dialog");
  const requests: unknown[] = [];
  page.on("request", (r) => {
    if (r.url().endsWith("/api/v1/plans/generate"))
      requests.push(r.postDataJSON());
  });
  await generate.focus();
  await page.keyboard.press("Enter");
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText(/计入目标模型预算/)).toBeVisible();
  expect(requests).toHaveLength(0);
  await page.keyboard.press("Escape");
  await expect(generate).toBeFocused();
  await generate.click();
  const finished = page.waitForResponse((r) =>
    r.url().endsWith("/api/v1/plans/generate"),
  );
  await dialog
    .getByRole("button", { name: "生成交付方案", exact: true })
    .click();
  const response = await finished;
  expect(response.ok(), await response.text()).toBeTruthy();
  const plan = await response.json();
  await expect(dialog).not.toBeVisible();
  expect(requests).toHaveLength(1);
  const input = requests[0] as {
    goalId: string;
    expectedRequirementRevision: number;
    key: string;
  };
  expect(input.goalId).toBe(goal.id);
  expect(input.expectedRequirementRevision).toBe(1);
  expect(input.key.length).toBeGreaterThan(10);
  await page.getByText("方案生成记录（1）", { exact: true }).click();
  await expect(page.getByText("方案已生成", { exact: true })).toBeVisible();
  await expect(page.getByText(/由模型生成 · 需求 v1/)).toBeVisible();
  const workspace = async () =>
    (
      await page.request.get("/api/v1/projects/" + project.id + "/workspace")
    ).json();
  expect(
    (await workspace()).filter((x: { kind: string }) => x.kind === "task"),
  ).toHaveLength(0);
  const replay = await page.request.post("/api/v1/plans/generate", {
    data: input,
    headers: { Origin: origin, "X-CSRF-Token": ownerSession.csrf },
  });
  expect(replay.ok()).toBeTruthy();
  expect((await replay.json()).id).toBe(plan.id);
  const screenshot = process.env.IS_PLAN_SCREENSHOTS;
  if (screenshot)
    await page.screenshot({
      path: screenshot + "/proposed.png",
      fullPage: true,
      animations: "disabled",
    });
  await page.getByRole("button", { name: "批准方案", exact: true }).click();
  await dialog.getByRole("button", { name: "确认批准", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  expect(
    (await workspace()).filter((x: { kind: string }) => x.kind === "task"),
  ).toHaveLength(1);
  expect(errors).toEqual([]);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
});

test("failed planning reuses its key, manual planning works and captured stale goals are rejected", async ({
  page,
  ownerSession,
}, info) => {
  const command = async (name: string, data: unknown) => {
    const response = await page.request.post("/api/v1/commands/" + name, {
      data,
      headers: { Origin: origin, "X-CSRF-Token": ownerSession.csrf },
    });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  const project = await command("projects.create", {
    title: "方案失败处理 " + info.project.name,
  });
  const makeGoal = async (title: string, outcome: string) => {
    const goal = await command("goals.create", {
      projectId: project.id,
      title,
      outcome,
      criteria: [{ id: "check", text: "只能访问本人预约" }],
      targets: ["web"],
    });
    await command("goals.approve", { id: goal.id });
    return goal;
  };
  const goal = await makeGoal(
    "失败后可人工编制",
    "local-fixture-failure 预约服务",
  );
  const other = await makeGoal("另一个已批准目标", "客户管理本人预约");
  await openPlans(page, project.id, goal.id, info.project.name === "mobile");
  const dialog = page.getByRole("dialog");
  const requests: { goalId: string; key: string }[] = [];
  page.on("request", (r) => {
    if (r.url().endsWith("/api/v1/plans/generate"))
      requests.push(r.postDataJSON());
  });
  await page.getByRole("button", { name: "生成方案", exact: true }).click();
  const confirm = dialog.getByRole("button", {
    name: "生成交付方案",
    exact: true,
  });
  await confirm.click();
  await expect(dialog.getByRole("alert")).toBeVisible();
  await expect(confirm).toBeEnabled();
  await confirm.click();
  await expect(dialog.getByRole("alert")).toContainText("未重复调用模型");
  expect(requests).toHaveLength(2);
  expect(requests[0].key).toBe(requests[1].key);
  await page.keyboard.press("Escape");
  await page.getByText("方案生成记录（1）", { exact: true }).click();
  await expect(page.getByText("生成未完成", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "人工编制", exact: true }).click();
  await dialog
    .getByLabel("方案与交付路径", { exact: true })
    .fill("由架构师编制预约功能方案并安排独立质量验证。");
  await dialog
    .getByRole("button", { name: "提交方案审阅", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByRole("button", { name: "批准方案", exact: true }),
  ).toBeVisible();
  await page.getByLabel("选择交付目标").selectOption(other.id);
  await expect(
    page.getByText("方案生成记录（1）", { exact: true }),
  ).not.toBeVisible();
  await page.getByRole("button", { name: "生成方案", exact: true }).click();
  await expect(dialog.getByText(/另一个已批准目标/)).toBeVisible();
  await command("goals.revise", { id: other.id, reason: "规划期间业务改版" });
  await confirm.click();
  await expect(dialog.getByRole("alert")).toContainText("需求版本");
  expect(requests).toHaveLength(3);
  expect(requests[2].goalId).toBe(other.id);
  expect(requests[2].key).not.toBe(requests[0].key);
  await page.keyboard.press("Escape");
  await expect(
    page.getByRole("button", { name: "生成方案", exact: true }),
  ).toBeDisabled();
});
