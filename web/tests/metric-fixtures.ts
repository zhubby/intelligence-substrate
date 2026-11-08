import { expect } from "./fixtures";
import type { Page } from "@playwright/test";

const origin = "http://127.0.0.1:18082";
export async function metricFixture(page: Page, csrf: string, suffix: string) {
  const command = async (name: string, data: unknown, token?: string) => {
    const r = await page.request.post("/api/v1/commands/" + name, {
      data,
      headers: token
        ? { Authorization: "Bearer " + token }
        : { Origin: origin, "X-CSRF-Token": csrf },
    });
    expect(r.ok(), await r.text()).toBeTruthy();
    return r.json();
  };
  const project = await command("projects.create", {
    title: "交付效能 " + suffix,
  });
  const createGoal = (title: string) =>
    command("goals.create", {
      projectId: project.id,
      title,
      outcome: "客户只能管理本人的预约。",
      targets: ["web"],
      criteria: [{ id: "own", text: "只管理本人预约", method: "automated" }],
      budget: 100,
    });
  const goal = await createGoal("预约交付观测");
  const empty = await createGoal("尚未观察的应用");
  await command("goals.approve", { id: goal.id });
  const repo = await command("repositories.create", {
    projectId: project.id,
    title: "预约源码",
  });
  const source = await command("repositories.commit", {
    id: repo.id,
    branch: "feature",
    message: "Synthetic source",
    files: { "index.html": "<h1>Metric fixture</h1>" },
  });
  const commit = source.data.lastCommit;
  const plan = await command("plans.propose", {
    goalId: goal.id,
    summary: "构建与独立验证",
    tasks: [
      {
        id: "build",
        title: "构建预约",
        kind: "build",
        role: "developer",
        runtime: "linux",
        repositoryId: repo.id,
        commit,
        target: "web",
        command: ["true"],
        outputPath: "app.html",
        budgetCents: 100,
      },
    ],
  });
  await command("plans.approve", { id: plan.id });
  const pipeline = await command("pipelines.create", {
    projectId: project.id,
    title: "浏览器验证",
    runtime: "linux",
    method: "automated",
    verificationEnvironment: "browser",
    command: ["true"],
    reportPath: "report.txt",
  });
  await command("quality.bind", {
    goalId: goal.id,
    expectedRequirementRevision: 1,
    expectedPlanId: plan.id,
    pipelineId: pipeline.id,
    target: "web",
    criteria: ["own"],
    budgetCents: 100,
    key: "metric-quality",
  });
  const credential = async (role: string) => {
    const worker = await command("workers.register", {
      projectId: project.id,
      title: role,
      roles: [role],
      runtime: "linux",
      capabilities: [role === "quality" ? "test" : "build"],
      trustedQuality: role === "quality",
    });
    const r = await page.request.post("/api/v1/tokens", {
      data: { projectId: project.id, workerId: worker.id },
      headers: { Origin: origin, "X-CSRF-Token": csrf },
    });
    expect(r.ok(), await r.text()).toBeTruthy();
    return (await r.json()).token as string;
  };
  const upload = async (
    token: string,
    run: any,
    type: string,
    body: string,
  ) => {
    const q = new URLSearchParams({
      goalId: goal.id,
      repositoryId: repo.id,
      commit,
      target: "web",
      type,
      name: type === "report" ? "观测报告.txt" : "预约应用.html",
      runId: run.id,
      leaseToken: run.data.leaseToken,
    });
    const r = await page.request.post("/api/v1/artifacts/upload?" + q, {
      data: Buffer.from(body),
      headers: {
        Authorization: "Bearer " + token,
        "Content-Type": "application/octet-stream",
      },
    });
    expect(r.ok(), await r.text()).toBeTruthy();
    return r.json();
  };
  // Synthetic Worker completions prove real workbench/API behavior. They are
  // neither a real browser business acceptance nor a deployed application.
  const developer = await credential("developer");
  const build = await command("tasks.claim", {}, developer);
  const artifact = await upload(
    developer,
    build,
    "package",
    "<h1>Metric fixture</h1>",
  );
  await command(
    "tasks.complete",
    {
      id: build.id,
      leaseToken: build.data.leaseToken,
      exitCode: 0,
      result: { artifactId: artifact.id, repositoryId: repo.id, commit },
    },
    developer,
  );
  const verifier = await credential("quality");
  const run = await command("tasks.claim", {}, verifier);
  const proof = await upload(
    verifier,
    run,
    "report",
    "Synthetic observation report; no real business results",
  );
  const finished = await command(
    "tasks.complete",
    {
      id: run.id,
      leaseToken: run.data.leaseToken,
      exitCode: 0,
      result: { reportArtifactId: proof.id },
    },
    verifier,
  );
  const release = await command("releases.create", {
    goalId: goal.id,
    version: "1.0.0",
    commit,
    artifactIds: [artifact.id],
  });
  return {
    project,
    goal,
    empty,
    artifact,
    proof,
    evidenceId: finished.data.evidenceId,
    release,
    command,
  };
}

export async function openMetrics(
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
    .getByRole("button", { name: "企业治理", exact: true })
    .click();
  await page.getByLabel("选择交付目标").selectOption(goal);
  await page.getByRole("tab", { name: "交付效能", exact: true }).click();
}
