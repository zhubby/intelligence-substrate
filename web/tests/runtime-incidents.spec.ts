import { test, expect } from "./fixtures";
import type { APIRequestContext } from "@playwright/test";

// Real API/SQL/auth/UI, synthetic package, QA and deployment metadata. Docker
// reachability and independent business acceptance are outside this fixture.
test("runtime incidents retain first ownership and trusted recovery history", async ({
  page,
  playwright,
  browser,
  ownerSession,
}, info) => {
  const origin = "http://127.0.0.1:18082";
  const pageErrors: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  const suffix = info.project.name + "-" + Date.now();
  const post = async (
    client: APIRequestContext,
    route: string,
    data: unknown,
    csrf: string,
  ) => {
    const response = await client.post(origin + "/api/v1/" + route, {
      data,
      headers: { Origin: origin, "X-CSRF-Token": csrf },
    });
    const value = await response.json();
    expect(response.ok(), JSON.stringify(value)).toBeTruthy();
    return value;
  };
  const command = (name: string, input: unknown) =>
    post(page.request, "commands/" + name, input, ownerSession.csrf);
  const project = await command("projects.create", {
    title: "运行事件 " + suffix,
  });
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "运行故障处理",
    outcome: "记录接手并保留恢复证据",
    targets: ["web"],
    criteria: [{ id: "works", text: "应用可用", method: "manual" }],
    budget: 30,
  });
  await command("goals.approve", { id: goal.id });
  const plan = await command("plans.propose", {
    goalId: goal.id,
    summary: "验证运行事件的受控发布前置条件",
    tasks: [
      {
        id: "build",
        title: "构建测试页面",
        kind: "code",
        role: "developer",
        runtime: "linux",
      },
    ],
  });
  await command("plans.approve", { id: plan.id });
  const repo = await command("repositories.create", {
    projectId: project.id,
    title: "incident-fixture",
  });
  const change = await command("repositories.commit", {
    id: repo.id,
    branch: "candidate",
    message: "Add synthetic runtime fixture",
    files: { "index.html": "<h1>Fixture</h1>" },
  });
  const qaEmail = "incident-qa-" + suffix + "@example.test";
  const identity = await post(
    page.request,
    "identities",
    {
      email: qaEmail,
      name: "运行质量人员",
      password: "incident-test-password-2026",
    },
    ownerSession.csrf,
  );
  await command("members.grant", {
    projectId: project.id,
    identityId: identity.id,
    roles: ["quality", "release"],
  });
  const qa = await playwright.request.newContext({ baseURL: origin });
  let viewerContext: Awaited<ReturnType<typeof browser.newContext>> | undefined;
  try {
    const qaSession = await post(
      qa,
      "auth/login",
      { email: qaEmail, password: "incident-test-password-2026" },
      "",
    );
    const upload = async (
      client: APIRequestContext,
      csrf: string,
      type: string,
    ) => {
      const query = new URLSearchParams({
        goalId: goal.id,
        repositoryId: repo.id,
        commit: change.data.lastCommit,
        target: "web",
        type,
        name: type + ".txt",
      });
      const response = await client.post(
        origin + "/api/v1/artifacts/upload?" + query,
        {
          data: Buffer.from(
            "Synthetic runtime UI fixture; no actual deployment",
          ),
          headers: {
            Origin: origin,
            "X-CSRF-Token": csrf,
            "Content-Type": "application/octet-stream",
          },
        },
      );
      expect(response.ok()).toBeTruthy();
      return response.json();
    };
    const artifact = await upload(page.request, ownerSession.csrf, "package");
    const proof = await upload(qa, qaSession.csrf, "report");
    await post(
      qa,
      "commands/evidence.record",
      {
        artifactId: artifact.id,
        proofArtifactId: proof.id,
        method: "manual",
        verificationEnvironment: "browser",
        criteria: ["works"],
        statement: "Synthetic UI fixture only",
      },
      qaSession.csrf,
    );
    const release = await command("releases.create", {
      goalId: goal.id,
      version: "1.0.0",
      commit: change.data.lastCommit,
      artifactIds: [artifact.id],
    });
    await post(
      qa,
      "commands/releases.approve",
      { id: release.id },
      qaSession.csrf,
    );
    const worker = await command("workers.register", {
      projectId: project.id,
      title: "运行探针节点",
      runtime: "linux",
      roles: ["release"],
      capabilities: ["publish"],
    });
    const credential = await post(
      page.request,
      "tokens",
      { projectId: project.id, workerId: worker.id },
      ownerSession.csrf,
    );
    const workerCommand = async (name: string, input: unknown) => {
      const response = await page.request.post(
        origin + "/api/v1/commands/" + name,
        {
          data: input,
          headers: { Authorization: "Bearer " + credential.token },
        },
      );
      const value = await response.json();
      expect(response.ok(), JSON.stringify(value)).toBeTruthy();
      return value;
    };
    const task = await command("releases.dispatch", {
      id: release.id,
      target: "web",
      workerId: worker.id,
      key: "runtime-ui",
    });
    const run = await workerCommand("tasks.claim", {});
    const externalId = "runtime:feedback:" + task.data.operationKey;
    await workerCommand("tasks.complete", {
      id: run.id,
      leaseToken: run.data.leaseToken,
      exitCode: 0,
      result: {
        state: "published",
        externalId,
        url: "https://feedback.example.test",
        runtime: {
          driver: "docker",
          application: "feedback",
          activationId: externalId,
          imageId: "sha256:" + "a".repeat(64),
          dataContract: "feedback-v1",
        },
      },
    });
    let sequence = 0,
      lastTime = 0;
    const report = async (health: string) => {
      lastTime = Math.max(Date.now(), lastTime + 1);
      return workerCommand("runtime.report", {
        externalId,
        releaseId: release.id,
        binding: release.data.binding,
        sequence: ++sequence,
        checkedAt: new Date(lastTime).toISOString(),
        health,
        statusCode: health === "healthy" ? 200 : 503,
        latencyMs: 12,
      });
    };
    for (let i = 0; i < 3; i++) await report("unhealthy");
    const state = await (
      await page.request.get("/api/v1/projects/" + project.id + "/workspace")
    ).json();
    const incident = state.find(
      (x: any) => x.kind === "incident" && x.status === "open",
    );
    expect(incident).toBeTruthy();
    await page.goto("/");
    if (info.project.name === "mobile")
      await page.getByRole("button", { name: "打开导航", exact: true }).click();
    await page.getByLabel("选择项目").selectOption(project.id);
    await page
      .getByRole("navigation", { name: "主导航" })
      .getByRole("button", { name: "构建与发布", exact: true })
      .click();
    const row = page.getByRole("article", {
      name: "运行事件 " + incident.id,
      exact: true,
    });
    const take = row.getByRole("button", { name: "确认接手", exact: true });
    await expect(take).toBeVisible();
    await take.click();
    const dialog = page.getByRole("dialog");
    await expect(
      dialog.getByRole("button", { name: "关闭对话框", exact: true }),
    ).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(dialog).not.toBeVisible();
    await expect(take).toBeFocused();
    await take.click();
    await dialog.getByRole("button", { name: "确认接手", exact: true }).click();
    await expect(dialog).toBeVisible();
    expect(
      await dialog
        .getByLabel("处理说明", { exact: true })
        .evaluate((node: HTMLTextAreaElement) => node.validity.valueMissing),
    ).toBeTruthy();
    await dialog
      .getByLabel("处理说明", { exact: true })
      .fill("已检查运行节点，正在核对失败探针与业务日志。");
    await dialog.getByRole("button", { name: "确认接手", exact: true }).click();
    await expect(dialog).not.toBeVisible();
    await expect(row.getByText(/已接手/)).toBeVisible();
    await expect(
      row.getByText("已检查运行节点，正在核对失败探针与业务日志。", {
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      row.getByRole("button", { name: "确认接手", exact: true }),
    ).toHaveCount(0);
    const stale = await page.request.post(
      origin + "/api/v1/commands/runtime.incident.acknowledge",
      {
        data: {
          id: incident.id,
          key: "new-owner",
          note: "Cannot replace original ownership",
        },
        headers: { Origin: origin, "X-CSRF-Token": ownerSession.csrf },
      },
    );
    expect(stale.status()).toBe(409);
    const result = await report("healthy");
    expect(result.data.verificationStatus).toBe("required");
    await expect(row).toBeVisible();
    await report("healthy");
    await expect(
      page.getByText("已恢复事件（1）", { exact: true }),
    ).toBeVisible({ timeout: 15000 });
    await page.getByText("已恢复事件（1）", { exact: true }).click();
    await expect(row.getByText("已恢复", { exact: true })).toBeVisible();
    await expect(row.getByText(/连续两次健康检查通过/)).toBeVisible();
    await expect(
      row.getByText("已检查运行节点，正在核对失败探针与业务日志。", {
        exact: true,
      }),
    ).toBeVisible();
    await page.screenshot({
      path:
        (process.env.IS_INCIDENT_SCREENSHOTS || "../work/screenshots") +
        "/" +
        info.project.name +
        "-runtime-incidents.png",
      fullPage: true,
    });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBeTruthy();
    // A distinct viewer can inspect active events but has no ownership action.
    for (let i = 0; i < 3; i++) await report("unhealthy");
    const viewerEmail = "incident-viewer-" + suffix + "@example.test";
    const viewer = await post(
      page.request,
      "identities",
      {
        email: viewerEmail,
        name: "运行观察员",
        password: "incident-viewer-password-2026",
      },
      ownerSession.csrf,
    );
    await command("members.grant", {
      projectId: project.id,
      identityId: viewer.id,
      roles: ["viewer"],
    });
    const viewerClient = await playwright.request.newContext({
      baseURL: origin,
    });
    try {
      const viewerSession = await post(
        viewerClient,
        "auth/login",
        { email: viewerEmail, password: "incident-viewer-password-2026" },
        "",
      );
      viewerContext = await browser.newContext({
        storageState: await viewerClient.storageState(),
        viewport: page.viewportSize() || undefined,
        isMobile: info.project.name === "mobile",
        hasTouch: info.project.name === "mobile",
      });
      const viewerPage = await viewerContext.newPage();
      viewerPage.on("pageerror", (error) => pageErrors.push(error.message));
      await viewerPage.goto(origin);
      if (info.project.name === "mobile")
        await viewerPage
          .getByRole("button", { name: "打开导航", exact: true })
          .click();
      await viewerPage.getByLabel("选择项目").selectOption(project.id);
      await viewerPage
        .getByRole("navigation", { name: "主导航" })
        .getByRole("button", { name: "构建与发布", exact: true })
        .click();
      await expect(
        viewerPage.getByRole("button", { name: "确认接手", exact: true }),
      ).toBeDisabled();
      const current = await (
        await viewerClient.get("/api/v1/projects/" + project.id + "/workspace")
      ).json();
      const open = current.find(
        (x: any) => x.kind === "incident" && x.status === "open",
      );
      const denied = await viewerClient.post(
        origin + "/api/v1/commands/runtime.incident.acknowledge",
        {
          data: {
            id: open.id,
            key: "viewer",
            note: "Viewer cannot claim ownership",
          },
          headers: { Origin: origin, "X-CSRF-Token": viewerSession.csrf },
        },
      );
      expect(denied.status()).toBe(403);
      expect(pageErrors).toEqual([]);
    } finally {
      await viewerClient.dispose();
    }
  } finally {
    await qa.dispose();
    if (viewerContext) await viewerContext.close();
  }
});
