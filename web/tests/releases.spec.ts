import { test, expect, type APIRequestContext } from "@playwright/test";

// Channel receipts and human attestations in this fixture are synthetic. This
// checks the workbench and authorization contract, not an actual store release.
test("processing continues under original approval and queries never count delivery", async ({
  page,
  playwright,
}, info) => {
  const origin = "http://127.0.0.1:18082";
  const channelVersion = "hosted:customer-feedback:web:" + "a".repeat(64);
  const ownerLogin = await page.request.post("/api/v1/auth/login", {
    data: {
      email: "browser@example.test",
      password: "browser-test-password-2026",
    },
    headers: { Origin: origin },
  });
  expect(ownerLogin.ok()).toBeTruthy();
  const ownerSession = await ownerLogin.json();
  const post = async (
    client: APIRequestContext,
    path: string,
    data: unknown,
    csrf: string,
  ) => {
    const response = await client.post(origin + "/api/v1/" + path, {
      data,
      headers: { Origin: origin, "X-CSRF-Token": csrf },
    });
    const result = await response.json();
    expect(response.ok(), JSON.stringify(result)).toBeTruthy();
    return result;
  };
  const command = (name: string, input: unknown) =>
    post(page.request, "commands/" + name, input, ownerSession.csrf);
  const project = await command("projects.create", {
    title: "渠道查询 " + info.project.name + " " + Date.now(),
  });
  const goal = await command("goals.create", {
    projectId: project.id,
    title: "查询审核中的 Web 发布",
    outcome: "保留真实审核状态和独立验收边界",
    targets: ["web"],
    criteria: [{ id: "page", text: "页面可访问", method: "manual" }],
    budget: 30,
  });
  await command("goals.approve", { id: goal.id });
  const plan = await command("plans.propose", {
    goalId: goal.id,
    summary: "发布已验证页面",
    tasks: [
      {
        id: "build",
        title: "构建页面",
        kind: "code",
        role: "developer",
        runtime: "linux",
      },
    ],
  });
  await command("plans.approve", { id: plan.id });
  const repo = await command("repositories.create", {
    projectId: project.id,
    title: "channel-fixture",
  });
  let commit = await command("repositories.commit", {
    id: repo.id,
    branch: "candidate",
    message: "Add channel contract fixture",
    files: { "index.html": "<h1>Channel fixture</h1>" },
  });
  const meta = {
    goalId: goal.id,
    repositoryId: repo.id,
    commit: commit.data.lastCommit,
    target: "web",
  };
  const upload = async (
    client: APIRequestContext,
    csrf: string,
    type: string,
    content: string,
  ) => {
    const query = new URLSearchParams({
      ...meta,
      type,
      name: type === "package" ? "site.html" : "fixture-report.txt",
    });
    const response = await client.post(
      origin + "/api/v1/artifacts/upload?" + query,
      {
        data: Buffer.from(content),
        headers: {
          Origin: origin,
          "X-CSRF-Token": csrf,
          "Content-Type": "application/octet-stream",
        },
      },
    );
    const result = await response.json();
    expect(response.ok(), JSON.stringify(result)).toBeTruthy();
    return result;
  };
  let artifact = await upload(
    page.request,
    ownerSession.csrf,
    "package",
    "<h1>Channel fixture</h1>",
  );
  const email = "qa-" + info.project.name + "-" + Date.now() + "@example.test";
  const qaIdentity = await post(
    page.request,
    "identities",
    { email, name: "独立发布验证", password: "qa-fixture-password-2026" },
    ownerSession.csrf,
  );
  await command("members.grant", {
    projectId: project.id,
    identityId: qaIdentity.id,
    name: qaIdentity.name,
    roles: ["quality", "release"],
  });
  const qa = await playwright.request.newContext({ baseURL: origin });
  try {
    const qaLogin = await post(
      qa,
      "auth/login",
      { email, password: "qa-fixture-password-2026" },
      "",
    );
    let proof = await upload(
      qa,
      qaLogin.csrf,
      "report",
      "Synthetic independent browser attestation for UI contract fixture only.",
    );
    await post(
      qa,
      "commands/evidence.record",
      {
        artifactId: artifact.id,
        proofArtifactId: proof.id,
        method: "manual",
        verificationEnvironment: "browser",
        criteria: ["page"],
        statement: "Synthetic fixture - not production acceptance",
      },
      qaLogin.csrf,
    );
    const release = await command("releases.create", {
      goalId: goal.id,
      version: "1.0.0",
      commit: commit.data.lastCommit,
      artifactIds: [artifact.id],
    });
    await post(
      qa,
      "commands/releases.approve",
      { id: release.id },
      qaLogin.csrf,
    );
    const worker = await command("workers.register", {
      projectId: project.id,
      title: "渠道状态节点",
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
          headers: {
            Origin: origin,
            Authorization: "Bearer " + credential.token,
          },
        },
      );
      const result = await response.json();
      expect(response.ok(), JSON.stringify(result)).toBeTruthy();
      return result;
    };
    await command("releases.dispatch", {
      id: release.id,
      target: "web",
      workerId: worker.id,
      key: "initial-publish",
    });
    const publishing = await workerCommand("tasks.claim", {});
    await workerCommand("tasks.complete", {
      id: publishing.id,
      leaseToken: publishing.data.leaseToken,
      exitCode: 0,
      result: {
        externalId: channelVersion,
        state: "processing",
      },
    });
    await page.goto("/");
    if (info.project.name === "mobile")
      await page.getByRole("button", { name: "打开导航", exact: true }).click();
    await page.getByLabel("选择项目").selectOption(project.id);
    await page
      .getByRole("navigation", { name: "主导航" })
      .getByRole("button", { name: "构建与发布", exact: true })
      .click();
    await expect(page.getByText("构建处理中", { exact: true })).toBeVisible();
    await page.screenshot({
      animations: "disabled",
      path:
        (process.env.IS_INCIDENT_SCREENSHOTS || "../work/screenshots") +
        "/" +
        info.project.name +
        "-processing.png",
      fullPage: true,
    });
    await expect(page.getByText("已交付", { exact: true })).toHaveCount(0);
    await page.getByRole("button", { name: "查询渠道", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByLabel("查询节点", { exact: true })).toHaveValue(
      worker.id,
    );
    await dialog
      .getByRole("button", { name: "提交状态查询", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await expect(
      page.getByRole("button", { name: "查询已排队", exact: true }),
    ).toBeDisabled();
    const querying = await workerCommand("tasks.claim", {});
    expect(querying.data.task.kind).toBe("publish_status");
    await workerCommand("tasks.complete", {
      id: querying.id,
      leaseToken: querying.data.leaseToken,
      exitCode: 0,
      result: {
        externalId: channelVersion,
        state: "processing",
        continueRequired: true,
        upstreamState: "COMPLETE",
      },
    });
    await expect(
      page.getByText("构建已处理，等待受控提审", { exact: true }),
    ).toBeVisible({ timeout: 15000 });
    const continuing = await workerCommand("tasks.claim", {});
    expect(continuing.data.task.kind).toBe("publish_continue");
    expect(continuing.data.task.operationKey).toBe(
      publishing.data.task.operationKey,
    );
    await expect(
      page.getByRole("button", { name: "提审处理中", exact: true }),
    ).toBeDisabled({ timeout: 15000 });
    await workerCommand("tasks.complete", {
      id: continuing.id,
      leaseToken: continuing.data.leaseToken,
      exitCode: 0,
      result: { externalId: channelVersion, state: "awaiting_review" },
    });
    await expect(page.getByText("等待审核", { exact: true })).toBeVisible({
      timeout: 15000,
    });
    await page.getByRole("button", { name: "查询渠道", exact: true }).click();
    await dialog
      .getByRole("button", { name: "提交状态查询", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    const reviewQuery = await workerCommand("tasks.claim", {});
    await workerCommand("tasks.complete", {
      id: reviewQuery.id,
      leaseToken: reviewQuery.data.leaseToken,
      exitCode: 0,
      result: {
        externalId: channelVersion,
        state: "rejected",
        upstreamState: "REJECTED",
        reason: "需要补充隐私说明",
      },
    });
    // The workspace refreshes external worker changes every six seconds.
    await expect(page.getByText("审核未通过", { exact: true })).toBeVisible({
      timeout: 15000,
    });
    await expect(
      page.getByText("需要补充隐私说明", { exact: true }),
    ).toBeVisible();
    await expect(page.getByText("已交付", { exact: true })).toHaveCount(0);
    await page.screenshot({
      animations: "disabled",
      path:
        (process.env.IS_INCIDENT_SCREENSHOTS || "../work/screenshots") +
        "/" +
        info.project.name +
        "-release.png",
      fullPage: true,
    });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBeTruthy();

    // The rejected package cannot reuse the earlier approval. Confirm repair
    // requirements in the workbench, then create a real descendant Git commit
    // and new package/evidence. Store observations here remain synthetic.
    const issuePanel = page.getByRole("region", {
      name: "拒审问题 " + release.id,
      exact: true,
    });
    await expect(
      issuePanel.getByText("待确认修复要求", { exact: true }),
    ).toBeVisible();
    await issuePanel
      .getByRole("button", { name: "确认修复要求", exact: true })
      .click();
    await dialog
      .getByLabel("修复要求", { exact: true })
      .fill("补齐隐私说明与提交前的隐私确认，重新构建并独立验证。");
    await page.screenshot({
      animations: "disabled",
      path:
        (process.env.IS_INCIDENT_SCREENSHOTS || "../work/screenshots") +
        "/" +
        info.project.name +
        "-repair-form.png",
      fullPage: true,
    });
    await dialog
      .getByRole("button", { name: "确认并纳入规划", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await expect(
      issuePanel.getByText("待重新规划", { exact: true }),
    ).toBeVisible();
    const repairPlan = await command("plans.propose", {
      goalId: goal.id,
      summary: "补齐隐私说明并重新构建、独立验证，再申请发布批准",
      tasks: [
        {
          id: "repair",
          title: "修复隐私流程",
          kind: "code",
          role: "developer",
          runtime: "linux",
        },
      ],
    });
    expect(repairPlan.data.releaseIssues).toHaveLength(1);
    expect(repairPlan.data.releaseIssues[0].source.releaseId).toBe(release.id);
    await command("plans.approve", { id: repairPlan.id });
    commit = await command("repositories.commit", {
      id: repo.id,
      branch: "repair-privacy",
      baseCommit: commit.data.lastCommit,
      message: "Repair privacy flow after channel rejection",
      files: {
        "index.html":
          "<h1>Channel fixture</h1><label>Confirm privacy before submitting</label>",
      },
    });
    meta.commit = commit.data.lastCommit;
    artifact = await upload(
      page.request,
      ownerSession.csrf,
      "package",
      "<h1>Repaired privacy flow</h1>",
    );
    proof = await upload(
      qa,
      qaLogin.csrf,
      "report",
      "Synthetic protocol fixture: independent verification of repaired privacy package",
    );
    await post(
      qa,
      "commands/evidence.record",
      {
        artifactId: artifact.id,
        proofArtifactId: proof.id,
        method: "manual",
        verificationEnvironment: "browser",
        criteria: ["page"],
        statement:
          "Synthetic UI fixture; separate real business execution required",
      },
      qaLogin.csrf,
    );
    await expect(
      issuePanel.getByText("修复交付中", { exact: true }),
    ).toBeVisible({ timeout: 15000 });
    await page.screenshot({
      animations: "disabled",
      path:
        (process.env.IS_INCIDENT_SCREENSHOTS || "../work/screenshots") +
        "/" +
        info.project.name +
        "-repair-planned.png",
      fullPage: true,
    });

    // Runtime metadata/probes below are synthetic protocol fixtures. Actual
    // Docker/backend acceptance is covered by scripts/verify-runtime.mjs.
    const runtimeVersion = async (version: string) => {
      const candidate = await command("releases.create", {
        goalId: goal.id,
        version,
        commit: commit.data.lastCommit,
        artifactIds: [artifact.id],
      });
      await post(
        qa,
        "commands/releases.approve",
        { id: candidate.id },
        qaLogin.csrf,
      );
      const task = await command("releases.dispatch", {
        id: candidate.id,
        target: "web",
        workerId: worker.id,
        key: version,
      });
      const run = await workerCommand("tasks.claim", {});
      const externalId = "runtime:feedback:" + task.data.operationKey;
      await workerCommand("tasks.complete", {
        id: run.id,
        leaseToken: run.data.leaseToken,
        exitCode: 0,
        result: {
          externalId,
          state: "published",
          url: "https://feedback.example.test",
          runtime: {
            driver: "docker",
            application: "feedback",
            activationId: externalId,
            imageId: "sha256:" + "c".repeat(64),
            dataContract: "feedback-v1",
          },
        },
      });
      const workspace = await (
        await page.request.get("/api/v1/projects/" + project.id + "/workspace")
      ).json();
      return workspace.find(
        (x: any) => x.kind === "deployment" && x.data.externalId === externalId,
      );
    };
    const previous = await runtimeVersion("2.0.0");
    const current = await runtimeVersion("2.1.0");
    for (let sequence = 1; sequence <= 3; sequence++)
      await workerCommand("runtime.report", {
        externalId: current.data.externalId,
        releaseId: current.data.releaseId,
        binding: current.data.binding,
        sequence,
        checkedAt: new Date().toISOString(),
        health: "unhealthy",
        statusCode: 503,
        latencyMs: 20,
      });
    await expect(
      page.getByRole("heading", { name: "应用运行", exact: true }),
    ).toBeVisible({ timeout: 15000 });
    await expect(
      page
        .getByRole("region", { name: "应用运行", exact: true })
        .getByRole("article", { name: "运行部署 " + current.id, exact: true })
        .getByText("健康异常", { exact: true }),
    ).toBeVisible({ timeout: 15000 });
    await expect(
      page.getByText("应用连续健康检查失败", { exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "提出回滚", exact: true }).click();
    await dialog
      .getByLabel("回滚目标", { exact: true })
      .selectOption(previous.id);
    await dialog
      .getByLabel("回滚原因", { exact: true })
      .fill("当前版本关键业务失败，需要恢复已验证的上一版本");
    await dialog
      .getByRole("button", { name: "提交回滚建议", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await expect(
      page.getByText("回滚建议等待独立发布人批准", { exact: true }),
    ).toBeVisible();
    const work = await (
      await page.request.get("/api/v1/projects/" + project.id + "/workspace")
    ).json();
    const proposal = work.find((x: any) => x.kind === "rollback");
    await page.context().addCookies((await qa.storageState()).cookies);
    await page.reload();
    if (info.project.name === "mobile")
      await page.getByRole("button", { name: "打开导航", exact: true }).click();
    await page
      .getByRole("navigation", { name: "主导航" })
      .getByRole("button", { name: "构建与发布", exact: true })
      .click();
    await page.getByRole("button", { name: "审阅回滚", exact: true }).click();
    await dialog
      .getByRole("button", { name: "批准本次回滚", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    const recovery = await workerCommand("tasks.claim", {});
    expect(recovery.data.task.rollbackId).toBe(proposal.id);
    await workerCommand("tasks.complete", {
      id: recovery.id,
      leaseToken: recovery.data.leaseToken,
      exitCode: 0,
      result: {
        externalId: recovery.data.task.expectedExternalId,
        state: "published",
      },
    });
    await expect(page.getByText("2.0.0（回滚）", { exact: true })).toBeVisible({
      timeout: 15000,
    });
    await expect(
      page.getByText("运行验收待完成", { exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "上传运行报告", exact: true })
      .click();
    await dialog.getByLabel("实际运行报告文件", { exact: true }).setInputFiles({
      name: "runtime-ui-report.txt",
      mimeType: "text/plain",
      buffer: Buffer.from(
        "Synthetic UI report fixture; not production acceptance",
      ),
    });
    await dialog
      .getByRole("button", { name: "上传运行报告", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    const afterUpload = await (
      await page.request.get("/api/v1/projects/" + project.id + "/workspace")
    ).json();
    const activeDeployment = afterUpload.find(
      (x: any) => x.kind === "deployment" && x.status === "active",
    );
    const uploadedReport = afterUpload.find(
      (x: any) => x.kind === "artifact" && x.title === "runtime-ui-report.txt",
    );
    expect(uploadedReport.data.deploymentId).toBe(activeDeployment.id);
    expect(uploadedReport.data.commit).toBe(activeDeployment.data.commit);
    await page.screenshot({
      animations: "disabled",
      path:
        (process.env.IS_INCIDENT_SCREENSHOTS || "../work/screenshots") +
        "/" +
        info.project.name +
        "-runtime.png",
      fullPage: true,
    });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBeTruthy();
  } finally {
    await qa.dispose();
  }
});
