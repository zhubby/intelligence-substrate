import { test, expect } from "./fixtures";
import type { Page } from "@playwright/test";

async function fixture(page: Page, csrf: string, active = false) {
  const command = async (name: string, data: unknown, token?: string) => {
    const r = await page.request.post("/api/v1/commands/" + name, {
      data,
      headers: token
        ? { Authorization: "Bearer " + token }
        : { Origin: "http://127.0.0.1:18082", "X-CSRF-Token": csrf },
    });
    expect(r.ok(), await r.text()).toBeTruthy();
    return r.json();
  };
  const project = await command("projects.create", { title: "Agent 进度恢复" });
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "预约交付",
    outcome: "保留已确认进度",
    targets: ["web"],
    criteria: [{ id: "resume", text: "未知操作等待核对" }],
    budget: 100,
  });
  await command("goals.approve", { id: goal.id });
  const plan = await command("plans.propose", {
    goalId: goal.id,
    summary: "受控恢复",
    tasks: [
      {
        id: "analysis",
        title: "分析预约契约",
        kind: "analysis",
        role: "developer",
        runtime: "linux",
      },
    ],
  });
  await command("plans.approve", { id: plan.id });
  const worker = await command("workers.register", {
    projectId: project.id,
    title: "原开发节点",
    roles: ["developer"],
    runtime: "linux",
    capabilities: ["analysis"],
  });
  const issued = await page.request.post("/api/v1/tokens", {
    data: { projectId: project.id, workerId: worker.id },
    headers: { Origin: "http://127.0.0.1:18082", "X-CSRF-Token": csrf },
  });
  expect(issued.ok()).toBeTruthy();
  const token = (await issued.json()).token;
  const run = await command("tasks.claim", {}, token);
  const source = { repositoryId: "", commit: "" };
  const first = await command(
    "agent_steps.prepare",
    {
      runId: run.id,
      leaseToken: run.data.leaseToken,
      step: 1,
      action: { action: "read", path: "contract.txt" },
      source,
    },
    token,
  );
  await command(
    "agent_steps.complete",
    {
      id: first.id,
      runId: run.id,
      leaseToken: run.data.leaseToken,
      observation: "已核查 UTC 预约契约，原始文件未改变。",
      checkpointId: "",
    },
    token,
  );
  const step = await command(
    "agent_steps.prepare",
    {
      runId: run.id,
      leaseToken: run.data.leaseToken,
      step: 2,
      action: { action: "run", command: ["/bin/fixture", "external-effect"] },
      source,
    },
    token,
  );
  if (!active)
    await command(
      "tasks.complete",
      {
        id: run.id,
        leaseToken: run.data.leaseToken,
        exitCode: 1,
        result: { blocked: true, reason: "合成浏览器场景：命令结果未明" },
      },
      token,
    );
  return { project, goal, worker, run, first, step, command };
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
  await page.getByRole("tab", { name: "执行记录", exact: true }).click();
}

test("owner preserves confirmed progress and reconciles the exact unknown action without retrying it", async ({
  page,
  ownerSession,
}, info) => {
  const f = await fixture(page, ownerSession.csrf);
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  await expect(page.getByText("已保存步骤 1", { exact: true })).toBeVisible();
  await page
    .getByRole("button", { name: "查看已确认进度", exact: true })
    .click();
  await expect(
    page
      .getByRole("dialog")
      .getByText("已核查 UTC 预约契约，原始文件未改变。", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "关闭对话框", exact: true }).click();
  await page
    .getByRole("region", { name: "Agent 操作待核对", exact: true })
    .getByRole("button", { name: /^核对操作：/ })
    .click();
  const dialog = page.getByRole("dialog");
  await expect(
    dialog.getByText(f.step.data.actionDigest, { exact: true }),
  ).toBeVisible();
  const confirm = dialog.getByRole("button", {
    name: "确认核对并放弃未明进度",
    exact: true,
  });
  await expect(confirm).toBeDisabled();
  await dialog
    .getByRole("button", { name: "停用原执行节点", exact: true })
    .click();
  await dialog
    .getByLabel("核对依据", { exact: true })
    .fill(
      "合成浏览器核对：已独立停止原进程并核查外部副作用，同意放弃本次未明的文件改动。",
    );
  await expect(confirm).toBeDisabled();
  await dialog.getByRole("checkbox").check();
  const shots = process.env.IS_QUALITY_SCREENSHOTS;
  if (shots)
    await page.screenshot({
      path: shots + "/agent-before.png",
      fullPage: true,
      animations: "disabled",
    });
  await confirm.click();
  await expect(
    dialog.getByText("已记录核对决定。重试将使用最后确认的进度与剩余预算。", {
      exact: true,
    }),
  ).toBeVisible();
  const state = await (
    await page.request.get(`/api/v1/projects/${f.project.id}/workspace`)
  ).json();
  expect(state.find((x: any) => x.id === f.step.id).status).toBe("abandoned");
  expect(state.find((x: any) => x.id === f.first.id).status).toBe("completed");
  expect(state.filter((x: any) => x.kind === "run")).toHaveLength(1);
  if (shots)
    await page.screenshot({
      path: shots + "/agent-after.png",
      fullPage: true,
      animations: "disabled",
    });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
});

test("disabled worker with a live original lease cannot reconcile its tool", async ({
  page,
  ownerSession,
}, info) => {
  const f = await fixture(page, ownerSession.csrf, true);
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  await page
    .getByRole("region", { name: "Agent 操作待核对", exact: true })
    .getByRole("button", { name: /^核对操作：/ })
    .click();
  const dialog = page.getByRole("dialog");
  await dialog
    .getByRole("button", { name: "停用原执行节点", exact: true })
    .click();
  await dialog
    .getByLabel("核对依据", { exact: true })
    .fill("节点已停用，原租约仍有效。");
  await dialog.getByRole("checkbox").check();
  await expect(
    dialog.getByRole("button", { name: "确认核对并放弃未明进度", exact: true }),
  ).toBeDisabled();
  await expect(
    dialog.getByText("原租约仍有效或待核实", { exact: true }),
  ).toBeVisible();
});
