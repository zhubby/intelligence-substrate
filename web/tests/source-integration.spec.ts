import { createHash } from "node:crypto";
import { test, expect } from "./fixtures";

test("integration details show prepared conflicts and explicit decisions", async ({ page, playwright, ownerSession: session }, info) => {
  const origin = "http://127.0.0.1:18082";
  const command = async (name: string, data: unknown) => {
    const response = await page.request.post("/api/v1/commands/" + name, {
      data, headers: { Origin: origin, "X-CSRF-Token": session.csrf },
    });
    const value = await response.json();
    expect(response.ok(), JSON.stringify(value)).toBeTruthy();
    return value;
  };
  const project = await command("projects.create", { title: "受控集成 " + info.project.name + " " + Date.now() });
  const repo = await command("repositories.create", { projectId: project.id, title: "integration" });
  const seed = await command("repositories.commit", { id: repo.id, branch: "seed", message: "Common file", files: { "shared.txt": "base\n" } });
  const goal = await command("goals.create", {
    projectId: project.id, title: "并行功能集成", outcome: "集成后再独立验收", targets: ["web"], budget: 100,
    criteria: [{ id: "both", text: "两个功能均可用" }],
  });
  await command("goals.approve", { id: goal.id });
  const plan = await command("plans.propose", {
    goalId: goal.id, summary: "并行开发与受控集成", tasks: [
      ...["left", "right"].map((side) => ({
        id: side, title: "实现 " + side, kind: "code", role: "developer", runtime: "linux",
        repositoryId: repo.id, branch: side, commit: seed.data.lastCommit, budgetCents: 10,
      })),
      { id: "join", title: "集成两个功能", kind: "code", sourceMode: "integrate", role: "developer", runtime: "linux", repositoryId: repo.id, dependsOn: ["left", "right"], budgetCents: 10 },
    ],
  });
  await command("plans.approve", { id: plan.id });
  const worker = await command("workers.register", { projectId: project.id, title: "集成验收节点", roles: ["developer"], capabilities: ["code"], runtime: "linux" });
  const tokenResponse = await page.request.post("/api/v1/tokens", {
    data: { projectId: project.id, workerId: worker.id }, headers: { Origin: origin, "X-CSRF-Token": session.csrf },
  });
  expect(tokenResponse.ok()).toBeTruthy();
  const credential = await tokenResponse.json();
  const client = await playwright.request.newContext({ baseURL: origin, extraHTTPHeaders: { Authorization: "Bearer " + credential.token } });
  try {
    const work = async (name: string, data: unknown) => {
      const response = await client.post("/api/v1/commands/" + name, { data });
      const result = await response.json();
      expect(response.ok(), JSON.stringify(result)).toBeTruthy();
      return result;
    };
    for (let i = 0; i < 2; i++) {
      const run = await work("tasks.claim", {});
      const source = run.data.task.sourceBinding;
      const committed = await work("repositories.commit", {
        id: repo.id, runId: run.id, leaseToken: run.data.leaseToken,
        branch: source.targetBranch, baseCommit: source.commit, expectedHead: source.expectedHead,
        message: "Implement " + source.targetBranch, files: { "shared.txt": source.targetBranch + "\n" },
      });
      await work("tasks.complete", { id: run.id, leaseToken: run.data.leaseToken, exitCode: 0, result: { repositoryId: repo.id, commit: committed.data.lastCommit } });
    }
    const run = await work("tasks.claim", {});
    const prepared = await work("sources.prepare", { runId: run.id, leaseToken: run.data.leaseToken });
    expect(prepared.data.integrationConflicts).toEqual(["shared.txt"]);
    await page.goto("/");
    if (info.project.name === "mobile") await page.getByRole("button", { name: "打开导航", exact: true }).click();
    await page.getByLabel("选择项目").selectOption(project.id);
    if (info.project.name === "mobile") await page.getByRole("button", { name: "关闭导航", exact: true }).click();
    await page.getByRole("tab", { name: "执行记录", exact: true }).click();
    const button = page.getByRole("button", { name: "查看工作项：集成两个功能", exact: true });
    await button.focus();
    await page.keyboard.press("Enter");
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByRole("heading", { name: "受控源码集成", exact: true })).toBeVisible();
    await expect(dialog.getByText("shared.txt", { exact: true })).toBeVisible();
    await expect(dialog.getByText("待处理", { exact: true })).toBeVisible();
    await expect(dialog.getByText(prepared.data.integrationWorkspaceCommit, { exact: true })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(button).toBeFocused();
    await work("sources.resolve", {
      runId: run.id, leaseToken: run.data.leaseToken,
      resolutions: [{ path: "shared.txt", digest: createHash("sha256").update("combined\n").digest("hex"), executable: false }],
    });
    await page.getByRole("button", { name: "刷新工作空间", exact: true }).click();
    await page.getByRole("tab", { name: "执行记录", exact: true }).click();
    await page.getByRole("button", { name: "查看工作项：集成两个功能", exact: true }).click();
    await expect(dialog.getByText("已记录决定", { exact: true })).toBeVisible();
    await expect(dialog.getByText("shared.txt", { exact: true })).toBeVisible();
    expect(await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBeTruthy();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBeTruthy();
    await page.screenshot({ path: "../work/screenshots/" + info.project.name + "-source-integration.png", fullPage: true });
    await page.keyboard.press("Escape");
    await expect(dialog).not.toBeVisible();
  } finally {
    await client.dispose();
  }
});
