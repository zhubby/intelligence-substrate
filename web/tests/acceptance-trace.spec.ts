import { test, expect } from "./fixtures";
import type { Page } from "@playwright/test";

const origin = "http://127.0.0.1:18082";
async function fixture(
  page: Page,
  csrf: string,
  suffix: string,
  report = true,
) {
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
    title: "验收追溯 " + suffix,
  });
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "客户预约交付",
    outcome: "客户只能管理自己的预约。",
    targets: ["web"],
    criteria: [{ id: "own", text: "只能管理自己的预约", method: "automated" }],
    budget: 100,
  });
  await command("goals.approve", { id: goal.id });
  const repo = await command("repositories.create", {
    projectId: project.id,
    title: "预约源码",
  });
  const source = await command("repositories.commit", {
    id: repo.id,
    branch: "feature",
    message: "Fixture source",
    files: { "index.html": "<h1>booking fixture</h1>" },
  });
  const commit = source.data.lastCommit;
  const plan = await command("plans.propose", {
    goalId: goal.id,
    summary: "构建后独立验证",
    tasks: [
      {
        id: "build",
        title: "构建预约应用",
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
    title: "预约验收",
    runtime: "linux",
    method: "automated",
    verificationEnvironment: "browser",
    command: ["true"],
    ...(report ? { reportPath: "report.txt" } : {}),
  });
  await command("quality.bind", {
    goalId: goal.id,
    expectedRequirementRevision: 1,
    expectedPlanId: plan.id,
    pipelineId: pipeline.id,
    target: "web",
    criteria: ["own"],
    budgetCents: 100,
    key: "trace-qa",
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
    const query = new URLSearchParams({
      goalId: goal.id,
      repositoryId: repo.id,
      commit,
      target: "web",
      type,
      name: type === "report" ? "预约验收.txt" : "预约应用.html",
      runId: run.id,
      leaseToken: run.data.leaseToken,
    });
    const r = await page.request.post("/api/v1/artifacts/upload?" + query, {
      data: Buffer.from(body),
      headers: {
        Authorization: "Bearer " + token,
        "Content-Type": "application/octet-stream",
      },
    });
    expect(r.ok(), await r.text()).toBeTruthy();
    return r.json();
  };
  // Browser fixture submits synthetic Worker completions. Go Worker suites
  // separately execute real tools; this proves workbench/API behavior only.
  const developer = await credential("developer");
  const build = await command("tasks.claim", {}, developer);
  const artifact = await upload(
    developer,
    build,
    "package",
    "<h1>booking fixture</h1>",
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
  const proof = report
    ? await upload(verifier, run, "report", "Local independent fixture report")
    : undefined;
  await command(
    "tasks.complete",
    {
      id: run.id,
      leaseToken: run.data.leaseToken,
      exitCode: 0,
      result: proof ? { reportArtifactId: proof.id } : {},
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
    command,
    project,
    goal,
    plan,
    repo,
    commit,
    build,
    run,
    artifact,
    proof,
    release,
  };
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
  await page.getByRole("tab", { name: "验收追溯", exact: true }).click();
}

test("criteria connect exact package, independent report, build and candidate without claiming publication", async ({
  page,
  ownerSession,
}, info) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const f = await fixture(page, ownerSession.csrf, info.project.name);
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  const section = page.getByRole("region", { name: "验收追溯", exact: true });
  await expect(section.getByText("1 / 1", { exact: true })).toBeVisible();
  await expect(
    section.getByText("只能管理自己的预约", { exact: true }),
  ).toBeVisible();
  await expect(section.getByText(f.commit, { exact: true })).toBeVisible();
  const artifact = section.getByRole("article", {
    name: "制品 " + f.artifact.id,
  });
  await expect(artifact.getByText(f.build.id, { exact: true })).toBeVisible();
  await expect(artifact.getByText(f.plan.id, { exact: true })).toBeVisible();
  await expect(
    artifact.getByText("报告附件已核验", { exact: true }),
  ).toBeVisible();
  const download = artifact.getByRole("link", {
    name: "下载验证报告",
    exact: true,
  });
  await expect(download).toHaveAttribute(
    "href",
    "/api/v1/artifacts/" + f.proof.id + "/content",
  );
  await download.focus();
  await expect(download).toBeFocused();
  expect(
    await (
      await page.request.get((await download.getAttribute("href"))!)
    ).text(),
  ).toBe("Local independent fixture report");
  const releases = section.getByRole("region", {
    name: "候选与发布",
    exact: true,
  });
  await expect(releases.getByText("待批准发布", { exact: true })).toBeVisible();
  await expect(
    releases.getByText("尚无当前绑定的发布批准", { exact: true }),
  ).toBeVisible();
  await expect(releases.getByText("待发布", { exact: true })).toBeVisible();
  const shot = process.env.IS_QUALITY_SCREENSHOTS;
  if (shot)
    await page.screenshot({
      path: shot + "/traceability.png",
      fullPage: true,
      animations: "disabled",
    });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
  expect(errors).toEqual([]);
});

test("baseline change shows gaps per required target and historical statuses stay historical", async ({
  page,
  ownerSession,
}, info) => {
  const f = await fixture(page, ownerSession.csrf, "版本 " + info.project.name);
  await f.command("goals.revise", {
    id: f.goal.id,
    reason: "新增客户端和验收范围",
    targets: ["web", "ios-appstore"],
    criteria: [{ id: "own", text: "预约数据需要加密", method: "automated" }],
  });
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  const section = page.getByRole("region", { name: "验收追溯", exact: true });
  await expect(section.getByText("0 / 2", { exact: true })).toBeVisible();
  await expect(
    section.getByText("预约数据需要加密", { exact: true }),
  ).toBeVisible();
  await expect(
    section.getByText("尚无此目标的候选制品", { exact: true }),
  ).toHaveCount(2);
  await expect(
    section.getByText("iOS · App Store", { exact: true }),
  ).toBeVisible();
  await section.getByLabel("追溯需求版本", { exact: true }).selectOption("1");
  await expect(section.getByText("历史版本不计入当前验收。")).toBeVisible();
  await expect(
    section.getByText("只能管理自己的预约", { exact: true }),
  ).toBeVisible();
  await expect(
    section.getByText("预约数据需要加密", { exact: true }),
  ).toHaveCount(0);
  await expect(section.getByText("已失效", { exact: true })).toHaveCount(2);
  await expect(section.getByText("0 / 1", { exact: true })).toBeVisible();
  await section.getByLabel("追溯需求版本", { exact: true }).selectOption("0");
  await expect(
    section.getByText("预约数据需要加密", { exact: true }),
  ).toBeVisible();
});

test("read errors can retry and log-only verification does not invent report attachments", async ({
  page,
  ownerSession,
}, info) => {
  const f = await fixture(
    page,
    ownerSession.csrf,
    "读取 " + info.project.name,
    false,
  );
  await page.route(
    "**/acceptance-trace?*",
    async (route) => {
      await route.fulfill({
        status: 503,
        json: {
          error: {
            code: "fixture_unavailable",
            message: "暂时无法读取验收记录",
          },
        },
      });
    },
    { times: 1 },
  );
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  const section = page.getByRole("region", { name: "验收追溯", exact: true });
  await expect(section.getByRole("alert")).toHaveText("暂时无法读取验收记录");
  const retry = section.getByRole("button", { name: "刷新追溯", exact: true });
  await retry.focus();
  await page.keyboard.press("Enter");
  await expect(section.getByText("1 / 1", { exact: true })).toBeVisible();
  await expect(
    section.getByText("仅有执行记录，未附报告", { exact: true }),
  ).toBeVisible();
  await expect(
    section.getByRole("link", { name: "下载验证报告", exact: true }),
  ).toHaveCount(0);
  await expect(section.getByText(f.run.id, { exact: true })).toBeVisible();
});

test("late responses cannot replace a newly selected goal", async ({
  page,
  ownerSession,
}, info) => {
  const f = await fixture(page, ownerSession.csrf, "切换 " + info.project.name);
  const other = await f.command("goals.create", {
    projectId: f.project.id,
    title: "独立新目标",
    outcome: "另一个业务场景",
    targets: ["web"],
    criteria: [{ id: "new", text: "新的独立验收标准" }],
  });
  await open(page, f.project.id, f.goal.id, info.project.name === "mobile");
  const section = page.getByRole("region", { name: "验收追溯", exact: true });
  await expect(section.getByText("1 / 1", { exact: true })).toBeVisible();
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  let seen!: () => void;
  const started = new Promise<void>((resolve) => {
    seen = resolve;
  });
  await page.route("**/acceptance-trace?*", async (route) => {
    if (new URL(route.request().url()).searchParams.get("goalId") !== f.goal.id)
      return route.continue();
    const response = await route.fetch();
    seen();
    await gate;
    await route.fulfill({ response }).catch(() => {});
  });
  await section.getByRole("button", { name: "刷新追溯", exact: true }).click();
  await started;
  await expect(section.getByRole("status")).toContainText("正在读取验收链路");
  await page.getByLabel("选择交付目标").selectOption(other.id);
  await expect(
    section.getByText("新的独立验收标准", { exact: true }),
  ).toBeVisible();
  release();
  await page.unrouteAll({ behavior: "wait" });
  await expect(
    section.getByText("只能管理自己的预约", { exact: true }),
  ).toHaveCount(0);
  await expect(
    section.getByText("新的独立验收标准", { exact: true }),
  ).toBeVisible();
});
