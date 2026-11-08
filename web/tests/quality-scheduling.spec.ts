import { test, expect } from "./fixtures";
import type { Page } from "@playwright/test";

const origin = "http://127.0.0.1:18082";
async function fixture(page: Page, csrf: string, suffix: string) {
  const command = async (name: string, data: unknown, token?: string) => {
    const response = await page.request.post("/api/v1/commands/" + name, {
      data,
      headers: token
        ? { Authorization: "Bearer " + token }
        : { Origin: origin, "X-CSRF-Token": csrf },
    });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  const project = await command("projects.create", {
    title: "自动质量 " + suffix,
  });
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "自动核验实际预约制品",
    outcome: "客户只能访问本人的预约。",
    targets: ["web"],
    criteria: [{ id: "own", text: "只能访问本人的预约", method: "automated" }],
    budget: 100,
  });
  await command("goals.approve", { id: goal.id });
  const repo = await command("repositories.create", {
    projectId: project.id,
    title: "booking",
  });
  const source = await command("repositories.commit", {
    id: repo.id,
    branch: "feature",
    message: "Browser API fixture source",
    files: { "index.html": "<h1>booking fixture</h1>" },
  });
  const commit = source.data.lastCommit;
  const plan = await command("plans.propose", {
    goalId: goal.id,
    summary: "构建并执行独立质量验证",
    tasks: [
      {
        id: "build",
        title: "构建预约页面",
        kind: "build",
        role: "developer",
        runtime: "linux",
        repositoryId: repo.id,
        commit,
        target: "web",
        command: ["true"],
        outputPath: "site.html",
        budgetCents: 100,
      },
    ],
  });
  await command("plans.approve", { id: plan.id });
  const pipeline = await command("pipelines.create", {
    projectId: project.id,
    title: "预约权限独立检查",
    runtime: "linux",
    method: "automated",
    verificationEnvironment: "browser",
    command: ["/bin/sh", "-c", 'test -s "$IS_ARTIFACT_PATH"'],
  });
  const credential = async (role: string, quality: boolean) => {
    const worker = await command("workers.register", {
      projectId: project.id,
      title: role,
      runtime: "linux",
      roles: [role],
      capabilities: [quality ? "test" : "build"],
      trustedQuality: quality,
    });
    const response = await page.request.post("/api/v1/tokens", {
      data: { projectId: project.id, workerId: worker.id },
      headers: { Origin: origin, "X-CSRF-Token": csrf },
    });
    expect(response.ok(), await response.text()).toBeTruthy();
    return (await response.json()).token as string;
  };
  const build = async () => {
    const token = await credential("developer", false);
    const run = await command("tasks.claim", {}, token);
    const query = new URLSearchParams({
      goalId: goal.id,
      repositoryId: repo.id,
      commit,
      target: "web",
      type: "package",
      name: "booking.html",
      runId: run.id,
      leaseToken: run.data.leaseToken,
    });
    const response = await page.request.post(
      "/api/v1/artifacts/upload?" + query,
      {
        data: Buffer.from("<h1>booking fixture</h1>"),
        headers: {
          Authorization: "Bearer " + token,
          "Content-Type": "application/octet-stream",
        },
      },
    );
    expect(response.ok(), await response.text()).toBeTruthy();
    const artifact = await response.json();
    await command(
      "tasks.complete",
      {
        id: run.id,
        leaseToken: run.data.leaseToken,
        exitCode: 0,
        result: { artifactId: artifact.id, repositoryId: repo.id, commit },
      },
      token,
    );
    return artifact;
  };
  return {
    command,
    credential,
    project,
    goal,
    plan,
    pipeline,
    build,
    repo,
    commit,
  };
}

async function openQuality(
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
    .getByRole("button", { name: /^质量与证据/ })
    .click();
  await page.getByLabel("选择交付目标").selectOption(goal);
}

// HTTP Worker completions in this browser fixture are synthetic. A separate Go
// Worker integration test executes actual package/repair/verification tools.
test("bound policies automatically queue exact builds and keep independent revocation", async ({
  page,
  ownerSession,
}, info) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const f = await fixture(page, ownerSession.csrf, info.project.name);
  await openQuality(
    page,
    f.project.id,
    f.goal.id,
    info.project.name === "mobile",
  );
  const bind = page.getByRole("button", { name: "自动绑定", exact: true });
  const dialog = page.getByRole("dialog");
  await bind.focus();
  await page.keyboard.press("Enter");
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText(/需求 v1/)).toBeVisible();
  await expect(
    dialog.getByLabel("只能访问本人的预约", { exact: true }),
  ).toBeChecked();
  await page.keyboard.press("Escape");
  await expect(bind).toBeFocused();
  await bind.click();
  await dialog
    .getByRole("button", { name: "确认自动绑定", exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  const section = page.getByRole("region", { name: "自动质量验证" });
  await expect(
    section.getByText("已绑定，等待独立质量节点轮询", { exact: true }),
  ).toBeVisible();
  const artifact = await f.build();
  const token = await f.credential("quality", true);
  const run = await f.command("tasks.claim", {}, token);
  expect(run.data.task.artifactId).toBe(artifact.id);
  expect(run.data.task.qualityDispatchId).toBeTruthy();
  await f.command(
    "tasks.complete",
    { id: run.id, leaseToken: run.data.leaseToken, exitCode: 0, result: {} },
    token,
  );
  expect((await f.command("tasks.claim", {}, token)).kind).toBe("worker");
  await page.getByRole("button", { name: "刷新工作空间", exact: true }).click();
  await expect(section.getByText("验证通过", { exact: true })).toBeVisible();
  await expect(
    section.getByText("自动验证已启用", { exact: true }),
  ).toBeVisible();
  await section
    .getByRole("button", { name: "查看验证记录", exact: true })
    .click();
  await expect(dialog.getByText(/需求 v1/)).toBeVisible();
  await page.keyboard.press("Escape");
  const shot = process.env.IS_QUALITY_SCREENSHOTS;
  if (shot)
    await page.screenshot({
      path: shot + "/verified.png",
      fullPage: true,
      animations: "disabled",
    });
  await section.getByRole("button", { name: "撤销绑定", exact: true }).click();
  await dialog
    .getByLabel("撤销原因", { exact: true })
    .fill("调整独立质量策略，保留历史验证记录。");
  await dialog.getByRole("button", { name: "确认撤销", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(section.getByText("已撤销", { exact: true })).toBeVisible();
  expect(errors).toEqual([]);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
});

test("protected report form and exact evidence downloads preserve keyboard access", async ({
  page,
  ownerSession,
}, info) => {
  const f = await fixture(page, ownerSession.csrf, "报告 " + info.project.name);
  await openQuality(
    page,
    f.project.id,
    f.goal.id,
    info.project.name === "mobile",
  );
  const add = page.getByRole("button", { name: "新增策略", exact: true });
  await add.focus();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog");
  const reportPath = dialog.getByLabel("报告文件路径（可选）", { exact: true });
  await expect(reportPath).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(add).toBeFocused();
  await add.click();
  await dialog
    .getByLabel("策略名称", { exact: true })
    .fill("带报告的浏览器验收");
  await dialog.getByLabel("执行程序", { exact: true }).fill("true");
  await dialog.getByLabel("参数（每行一项）", { exact: true }).fill("");
  await dialog
    .getByLabel("实际验证环境", { exact: true })
    .selectOption("browser");
  await reportPath.fill("../outside.json");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("报告路径");
  await reportPath.fill("quality/browser.tar.gz");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const row = page
    .locator(".resource-row")
    .filter({ hasText: "带报告的浏览器验收" })
    .filter({
      has: page.getByRole("button", { name: "自动绑定", exact: true }),
    });
  await expect(row).toContainText("quality/browser.tar.gz");
  if (info.project.name === "mobile") {
    const reportLabel = row.getByText("报告 quality/browser.tar.gz", {
      exact: true,
    });
    expect((await reportLabel.boundingBox())?.width).toBeGreaterThan(200);
  }
  await row.getByRole("button", { name: "自动绑定", exact: true }).click();
  await dialog
    .getByRole("button", { name: "确认自动绑定", exact: true })
    .click();
  const artifact = await f.build();
  const token = await f.credential("quality", true);
  const run = await f.command("tasks.claim", {}, token);
  const query = new URLSearchParams({
    goalId: f.goal.id,
    repositoryId: f.repo.id,
    commit: f.commit,
    target: "web",
    type: "report",
    name: "browser.tar.gz",
    runId: run.id,
    leaseToken: run.data.leaseToken,
  });
  const upload = await page.request.post("/api/v1/artifacts/upload?" + query, {
    data: Buffer.from("browser-report-fixture"),
    headers: {
      Authorization: "Bearer " + token,
      "Content-Type": "application/octet-stream",
    },
  });
  expect(upload.ok(), await upload.text()).toBeTruthy();
  const proof = await upload.json();
  expect(proof.data.testedArtifactId).toBe(artifact.id);
  await f.command(
    "tasks.complete",
    {
      id: run.id,
      leaseToken: run.data.leaseToken,
      exitCode: 0,
      result: { reportArtifactId: proof.id },
    },
    token,
  );
  await page.getByRole("button", { name: "刷新工作空间", exact: true }).click();
  const evidence = page
    .locator(".evidence-row")
    .filter({ has: page.getByText("带报告的浏览器验收", { exact: true }) });
  const download = evidence.getByRole("link", {
    name: "下载验证报告",
    exact: true,
  });
  await expect(download).toHaveAttribute(
    "href",
    "/api/v1/artifacts/" + proof.id + "/content",
  );
  expect(
    await (
      await page.request.get((await download.getAttribute("href")) as string)
    ).text(),
  ).toBe("browser-report-fixture");
  await download.focus();
  await expect(download).toBeFocused();
  const section = page.getByRole("region", { name: "自动质量验证" });
  const record = section.getByRole("button", {
    name: "查看验证记录",
    exact: true,
  });
  await record.click();
  await expect(
    dialog.getByRole("link", { name: "下载验证报告", exact: true }),
  ).toHaveAttribute("href", "/api/v1/artifacts/" + proof.id + "/content");
  await page.keyboard.press("Escape");
  await expect(record).toBeFocused();
  const shot = process.env.IS_QUALITY_SCREENSHOTS;
  if (shot)
    await page.screenshot({
      path: shot + "/reports.png",
      fullPage: true,
      animations: "disabled",
    });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
});

test("missing required report remains visible as blocked without passing evidence", async ({
  page,
  ownerSession,
}, info) => {
  const f = await fixture(
    page,
    ownerSession.csrf,
    "缺报告 " + info.project.name,
  );
  const pipeline = await f.command("pipelines.create", {
    projectId: f.project.id,
    title: "必须保存业务报告",
    runtime: "linux",
    method: "automated",
    verificationEnvironment: "browser",
    command: ["true"],
    reportPath: "quality/report.json",
  });
  await f.command("quality.bind", {
    goalId: f.goal.id,
    expectedRequirementRevision: f.goal.revision,
    expectedPlanId: f.plan.id,
    pipelineId: pipeline.id,
    target: "web",
    criteria: ["own"],
    budgetCents: 100,
    key: "required-report",
  });
  await f.build();
  const token = await f.credential("quality", true);
  const run = await f.command("tasks.claim", {}, token);
  await f.command(
    "tasks.complete",
    {
      id: run.id,
      leaseToken: run.data.leaseToken,
      exitCode: 1,
      result: {
        blocked: true,
        reason: "required quality report: quality/report.json does not exist",
      },
      log: "tool exited 0, required report missing",
    },
    token,
  );
  await openQuality(
    page,
    f.project.id,
    f.goal.id,
    info.project.name === "mobile",
  );
  const section = page.getByRole("region", { name: "自动质量验证" });
  await expect(section.getByText("验证阻塞", { exact: true })).toBeVisible();
  await expect(section.getByText(/required quality report/)).toBeVisible();
  await section
    .getByRole("button", { name: "查看验证记录", exact: true })
    .click();
  await expect(
    page
      .getByRole("dialog")
      .getByText("tool exited 0, required report missing", { exact: true }),
  ).toBeVisible();
  await expect(
    page
      .getByRole("dialog")
      .getByRole("link", { name: "下载验证报告", exact: true }),
  ).toHaveCount(0);
  await page.keyboard.press("Escape");
  await expect(page.locator(".evidence-row")).toHaveCount(0);
});

test("policy dialogs capture target baseline and reject stale submissions without changing scope", async ({
  page,
  ownerSession,
}, info) => {
  const f = await fixture(page, ownerSession.csrf, "版本 " + info.project.name);
  await openQuality(
    page,
    f.project.id,
    f.goal.id,
    info.project.name === "mobile",
  );
  const dialog = page.getByRole("dialog");
  const inputs: {
    goalId: string;
    expectedRequirementRevision: number;
    expectedPlanId: string;
    key: string;
  }[] = [];
  page.on("request", (r) => {
    if (r.url().endsWith("/commands/quality.bind"))
      inputs.push(r.postDataJSON());
  });
  await page.getByRole("button", { name: "自动绑定", exact: true }).click();
  await f.command("goals.revise", {
    id: f.goal.id,
    reason: "绑定期间业务范围变更",
  });
  const save = dialog.getByRole("button", {
    name: "确认自动绑定",
    exact: true,
  });
  await save.click();
  await expect(dialog.getByRole("alert")).toContainText("需求版本");
  await save.click();
  await expect(dialog.getByRole("alert")).toContainText("需求版本");
  expect(inputs).toHaveLength(2);
  expect(inputs[0].goalId).toBe(f.goal.id);
  expect(inputs[0].expectedRequirementRevision).toBe(1);
  expect(inputs[0].expectedPlanId).toBe(f.plan.id);
  expect(inputs[0].key).toBe(inputs[1].key);
  await page.keyboard.press("Escape");
  await expect(
    page.getByRole("button", { name: "自动绑定", exact: true }),
  ).toBeDisabled();
  const response = await page.request.get(
    "/api/v1/projects/" + f.project.id + "/workspace",
  );
  expect(
    (await response.json()).filter(
      (r: { kind: string }) => r.kind === "quality_policy",
    ),
  ).toHaveLength(0);
});
