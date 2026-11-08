import { test, expect } from "./fixtures";

// Synthetic packages, channel observations and device attestations exercise
// the actual workbench/API, without claiming an Apple upload or real device.
for (const screenshotsScope of [false, true]) {
  const scope = screenshotsScope ? "screenshots" : "text";
  test(`independent ${scope} approval preserves the verified Web target and original iOS package`, async ({
    page,
    ownerSession,
  }, info) => {
    const origin = "http://127.0.0.1:18082";
    const screenshots = process.env.IS_INCIDENT_SCREENSHOTS;
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    const headers = { Origin: origin, "X-CSRF-Token": ownerSession.csrf };
    const command = async (name: string, data: any, token?: string) => {
      const response = await page.request.post("/api/v1/commands/" + name, {
        data,
        headers: token ? { Authorization: "Bearer " + token } : headers,
      });
      expect(response.ok(), await response.text()).toBeTruthy();
      return response.json();
    };
    const project = await command("projects.create", {
      title: "商店资料修订 " + info.project.name,
    });
    const goal = await command("goals.create", {
      projectId: project.id,
      title: "多端资料修订验收",
      outcome: "原构建下修正商店隐私说明",
      targets: ["web", "ios-appstore"],
      criteria: [{ id: "content", text: "内容可访问", method: "manual" }],
      budget: 100,
    });
    await command("goals.approve", { id: goal.id });
    const plan = await command("plans.propose", {
      goalId: goal.id,
      summary: "验证多端资料修订",
      tasks: [
        {
          id: "analysis",
          title: "核查原构建",
          kind: "analysis",
          role: "analysis",
          runtime: "linux",
        },
      ],
    });
    await command("plans.approve", { id: plan.id });
    const repo = await command("repositories.create", {
      projectId: project.id,
      title: "资料修订样本",
    });
    const source = await command("repositories.commit", {
      id: repo.id,
      branch: "feature",
      message: "Synthetic multi-client source",
      files: { "README.md": "Synthetic fixture, not a real IPA" },
    });
    const commit = source.data.lastCommit;
    const tokenResponse = await page.request.post("/api/v1/tokens", {
      data: { projectId: project.id, name: "资料提案 Agent" },
      headers,
    });
    expect(tokenResponse.ok(), await tokenResponse.text()).toBeTruthy();
    const author = (await tokenResponse.json()).token;
    const rpc = async (name: string, args: any) => {
      const response = await page.request.post("/mcp", {
        data: {
          jsonrpc: "2.0",
          id: 1,
          method: "tools/call",
          params: { name, arguments: args },
        },
        headers: { Authorization: "Bearer " + author },
      });
      expect(response.ok(), await response.text()).toBeTruthy();
      return (await response.json()).result;
    };
    const upload = async (
      target: string,
      type: string,
      name: string,
      body: string,
      token?: string,
    ) => {
      const q = new URLSearchParams({
        goalId: goal.id,
        repositoryId: repo.id,
        commit,
        target,
        type,
        name,
        mediaType: "application/json",
      });
      const response = await page.request.post(
        "/api/v1/artifacts/upload?" + q,
        {
          data: Buffer.from(body),
          headers: {
            ...(token ? { Authorization: "Bearer " + token } : headers),
            "Content-Type": "application/octet-stream",
          },
        },
      );
      expect(response.ok(), await response.text()).toBeTruthy();
      return response.json();
    };
    const web = await upload(
      "web",
      "package",
      "Web样本",
      "synthetic Web",
      author,
    );
    const ios = await upload(
      "ios-appstore",
      "package",
      "原iOS包",
      "synthetic IPA",
      author,
    );
    const webProof = await upload(
      "web",
      "report",
      "Web验证样本",
      "synthetic browser attestation",
    );
    const iosProof = await upload(
      "ios-appstore",
      "report",
      "设备验证样本",
      "synthetic iOS attestation",
    );
    await command("evidence.record", {
      artifactId: web.id,
      proofArtifactId: webProof.id,
      method: "manual",
      verificationEnvironment: "browser",
      criteria: ["content"],
      statement: "Synthetic local browser fixture",
    });
    await command("evidence.record", {
      artifactId: ios.id,
      proofArtifactId: iosProof.id,
      method: "manual",
      verificationEnvironment: "simulator",
      criteria: ["content"],
      statement: "Synthetic simulator fixture",
    });
    await command("evidence.record", {
      artifactId: ios.id,
      proofArtifactId: iosProof.id,
      method: "device",
      verificationEnvironment: "ios-device",
      purpose: "environment",
      statement: "Synthetic device fixture; no real device verification",
    });
    const old = await upload(
      "ios-appstore",
      "report",
      "原商店资料.json",
      JSON.stringify({ schemaVersion: 1, description: "Old privacy URL" }),
      author,
    );
    const next = await upload(
      "ios-appstore",
      "report",
      "修订隐私说明.json",
      JSON.stringify({
        schemaVersion: 1,
        description: "Corrected privacy URL",
      }),
      author,
    );
    const release = await command("releases.create", {
      goalId: goal.id,
      version: "1.0.0",
      commit,
      artifactIds: [web.id, ios.id],
      storeMetadataArtifactIds: [old.id],
    });
    await command("releases.approve", { id: release.id });
    await command("releases.transition", {
      id: release.id,
      target: "web",
      state: "published",
      externalId: "synthetic-web-1",
      proofArtifactId: webProof.id,
    });
    // Separate observing worker is required for independent publication evidence.
    const pub = await command("workers.register", {
      projectId: project.id,
      title: "原可信发布节点",
      runtime: "linux",
      roles: ["release"],
      capabilities: ["publish"],
    });
    const creds = await page.request.post("/api/v1/tokens", {
      data: { projectId: project.id, workerId: pub.id },
      headers,
    });
    expect(creds.ok()).toBeTruthy();
    const worker = (await creds.json()).token;
    // Web observation was made by this human; use a separate QA Agent only for
    // read access later. For verified Web, record through a second human below.
    const qaResponse = await page.request.post("/api/v1/identities", {
      data: {
        email:
          "material-qa-" + info.project.name + "-" + scope + "@example.test",
        name: "独立验收人",
        password: "material-qa-password-2026",
      },
      headers,
    });
    expect(qaResponse.ok(), await qaResponse.text()).toBeTruthy();
    const qa = await qaResponse.json();
    await command("members.grant", {
      projectId: project.id,
      identityId: qa.id,
      name: qa.name,
      roles: ["quality"],
    });
    const qaContext = await page.context().browser()!.newContext();
    const qaLogin = await qaContext.request.post(
      origin + "/api/v1/auth/login",
      {
        data: {
          email:
            "material-qa-" + info.project.name + "-" + scope + "@example.test",
          password: "material-qa-password-2026",
        },
        headers: { Origin: origin },
      },
    );
    expect(qaLogin.ok()).toBeTruthy();
    const qaSession = await qaLogin.json();
    const qaCommand = async (name: string, data: any) => {
      const response = await qaContext.request.post(
        origin + "/api/v1/commands/" + name,
        { data, headers: { Origin: origin, "X-CSRF-Token": qaSession.csrf } },
      );
      expect(response.ok(), await response.text()).toBeTruthy();
      return response.json();
    };
    const webEv = await qaCommand("evidence.record", {
      artifactId: web.id,
      proofArtifactId: webProof.id,
      method: "manual",
      verificationEnvironment: "browser",
      purpose: "publication",
      externalId: "synthetic-web-1",
      statement: "Synthetic independent Web publication",
    });
    await qaCommand("releases.verify", {
      id: release.id,
      target: "web",
      evidenceId: webEv.id,
    });
    await command("releases.dispatch", {
      id: release.id,
      target: "ios-appstore",
      workerId: pub.id,
      key: "upload",
    });
    let run = await command("tasks.claim", {}, worker);
    await command(
      "tasks.complete",
      {
        id: run.id,
        leaseToken: run.data.leaseToken,
        exitCode: 0,
        result: { state: "awaiting_review", externalId: "apple:123:upload1" },
      },
      worker,
    );
    await command("releases.refresh", {
      id: release.id,
      target: "ios-appstore",
      key: "rejected",
    });
    run = await command("tasks.claim", {}, worker);
    await command(
      "tasks.complete",
      {
        id: run.id,
        leaseToken: run.data.leaseToken,
        exitCode: 0,
        result: {
          state: "rejected",
          externalId: "apple:123:upload1",
          reason: "隐私政策链接需修订",
          upstreamState: "METADATA_REJECTED",
        },
      },
      worker,
    );
    const workspace = async () =>
      (
        await page.request.get("/api/v1/projects/" + project.id + "/workspace")
      ).json();
    let items = await workspace();
    const issue = items.find((x: any) => x.kind === "release_issue");
    const open = async () => {
      await page.goto("/");
      if (info.project.name === "mobile")
        await page
          .getByRole("button", { name: "打开导航", exact: true })
          .click();
      await page.getByLabel("选择项目").selectOption(project.id);
      await page
        .getByRole("navigation", { name: "主导航" })
        .getByRole("button", { name: "构建与发布", exact: true })
        .click();
      await page.getByLabel("选择交付目标").selectOption(goal.id);
    };
    await open();
    const panel = page.getByRole("region", { name: "拒审问题 " + release.id });
    await panel
      .getByRole("button", { name: "确认修复要求", exact: true })
      .click();
    await page
      .getByLabel("修复方式")
      .selectOption(screenshotsScope ? "store-media" : "store-text");
    await page
      .getByLabel("修复要求", { exact: true })
      .fill(
        screenshotsScope
          ? "替换不准确截图并修订说明，保留原构建"
          : "仅修订商店隐私政策链接，保留原构建与截图",
      );
    await page
      .getByRole("button", { name: "确认修复方式", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toBeHidden();
    const proposal = await rpc("platform_command", {
      command: "store_revisions.propose",
      input: {
        issueId: issue.id,
        metadataArtifactId: next.id,
        key: "agent-proposal",
      },
    });
    expect(proposal.isError).toBeFalsy();
    const revision = proposal.structuredContent.resource;
    const read = await rpc("get_resource", { id: revision.id });
    expect(read.structuredContent.resource.data.binding).toBe(
      revision.data.binding,
    );
    for (const name of [
      "store_revisions.approve",
      "store_revisions.dispatch",
    ]) {
      const decision = await rpc("platform_command", {
        command: name,
        input: { id: revision.id },
      });
      expect(decision.isError).toBe(true);
    }
    const denied = await page.request.post(
      "/api/v1/commands/store_revisions.approve",
      {
        data: { id: revision.id, expectedBinding: revision.data.binding },
        headers: { Authorization: "Bearer " + author },
      },
    );
    expect(denied.status()).toBe(403);
    const noCSRF = await page.request.post(
      "/api/v1/commands/store_revisions.approve",
      {
        data: { id: revision.id, expectedBinding: revision.data.binding },
        headers: { Origin: origin },
      },
    );
    expect(noCSRF.status()).toBe(403);
    const wrongRole = await qaContext.request.post(
      origin + "/api/v1/commands/store_revisions.approve",
      {
        data: { id: revision.id, expectedBinding: revision.data.binding },
        headers: { Origin: origin, "X-CSRF-Token": qaSession.csrf },
      },
    );
    expect(wrongRole.status()).toBe(403);
    await open();
    const revPanel = page.getByRole("article", {
      name: "资料修订 " + revision.id,
    });
    if (screenshotsScope)
      await expect(
        page.getByRole("heading", {
          name: "App Store 截图与资料修订",
          exact: true,
        }),
      ).toBeVisible();
    await revPanel
      .getByRole("button", { name: "审阅并批准资料", exact: true })
      .click();
    await page.getByRole("button", { name: "确认批准", exact: true }).click();
    await expect(page.getByRole("dialog")).toBeHidden();
    await revPanel
      .getByRole("button", { name: "执行资料修订", exact: true })
      .click();
    await page.getByRole("button", { name: "确认执行", exact: true }).click();
    await expect(page.getByRole("dialog")).toBeHidden();
    run = await command("tasks.claim", {}, worker);
    expect(run.data.task.kind).toBe("publish_materials");
    expect(run.data.task.artifactId).toBe(ios.id);
    expect(run.data.task.materialScope).toBe(
      screenshotsScope ? "screenshots" : undefined,
    );
    if (screenshotsScope) {
      await command(
        "tasks.complete",
        {
          id: run.id,
          leaseToken: run.data.leaseToken,
          exitCode: 0,
          result: { state: "processing", externalId: "apple:123:upload1" },
        },
        worker,
      );
      await open();
      await expect(
        page.getByText("当前渠道状态：等待新截图处理，尚未重新提审", {
          exact: true,
        }),
      ).toBeVisible();
      if (screenshots)
        await page.screenshot({
          path: screenshots + "/screenshots-processing.png",
          fullPage: true,
          animations: "disabled",
        });
      const idle = await command("tasks.claim", {}, worker);
      expect(idle.kind).not.toBe("run");
      await command("releases.refresh", {
        id: release.id,
        target: "ios-appstore",
        key: "images-ready",
      });
      run = await command("tasks.claim", {}, worker);
      expect(run.data.task.materialScope).toBe("screenshots");
      await command(
        "tasks.complete",
        {
          id: run.id,
          leaseToken: run.data.leaseToken,
          exitCode: 0,
          result: {
            state: "processing",
            externalId: "apple:123:upload1",
            continueRequired: true,
          },
        },
        worker,
      );
      run = await command("tasks.claim", {}, worker);
      expect(run.data.task.kind).toBe("publish_continue");
      expect(run.data.task.materialScope).toBe("screenshots");
      expect(run.data.task.publicationBinding).toBe(revision.data.binding);
    }
    await command(
      "tasks.complete",
      {
        id: run.id,
        leaseToken: run.data.leaseToken,
        exitCode: 0,
        result: { state: "awaiting_review", externalId: "apple:123:upload1" },
      },
      worker,
    );
    await open();
    await expect(
      page.getByText("当前渠道状态：等待渠道审核", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "候选原批准资料", exact: true }),
    ).toBeVisible();
    await revPanel.getByText("批准与资料来源", { exact: true }).click();
    if (screenshots)
      await page.screenshot({
        path: screenshots + "/" + scope + "-material-awaiting-review.png",
        fullPage: true,
        animations: "disabled",
      });
    items = await workspace();
    let current = items.find((x: any) => x.id === release.id);
    expect(current.data.binding).toBe(release.data.binding);
    expect(
      current.data.targets.find((x: any) => x.target === "web").status,
    ).toBe("verified");
    expect(items.find((x: any) => x.id === issue.id).status).not.toBe(
      "resolved",
    );
    await command("releases.refresh", {
      id: release.id,
      target: "ios-appstore",
      key: "published",
    });
    run = await command("tasks.claim", {}, worker);
    expect(run.data.task.metadataDigest).toBe(next.data.digest);
    await command(
      "tasks.complete",
      {
        id: run.id,
        leaseToken: run.data.leaseToken,
        exitCode: 0,
        result: { state: "published", externalId: "apple:123:upload1" },
      },
      worker,
    );
    const iosEv = await qaCommand("evidence.record", {
      artifactId: ios.id,
      proofArtifactId: iosProof.id,
      method: "device",
      verificationEnvironment: "ios-device",
      purpose: "publication",
      externalId: "apple:123:upload1",
      statement:
        "Synthetic independent publication of corrected store material",
      releaseId: release.id,
      publicationBinding: revision.data.binding,
    });
    await qaCommand("releases.verify", {
      id: release.id,
      target: "ios-appstore",
      evidenceId: iosEv.id,
    });
    await qaContext.close();
    await open();
    await expect(
      page.getByText("资料发布已验证", { exact: true }),
    ).toBeVisible();
    items = await workspace();
    expect(items.find((x: any) => x.id === issue.id).status).toBe("resolved");
    current = items.find((x: any) => x.id === release.id);
    expect(current.status).toBe("delivered");
    expect(current.data.binding).toBe(release.data.binding);
    expect(
      current.data.targets.find((x: any) => x.target === "web").status,
    ).toBe("verified");
    const revoked = await page.request.post("/api/v1/tokens/revoke", {
      data: {
        projectId: project.id,
        id: await page.evaluate(async (value) => {
          const bytes = await crypto.subtle.digest(
            "SHA-256",
            new TextEncoder().encode(JSON.stringify(value)),
          );
          return Array.from(new Uint8Array(bytes), (b) =>
            b.toString(16).padStart(2, "0"),
          ).join("");
        }, author),
      },
      headers,
    });
    expect(revoked.ok(), await revoked.text()).toBeTruthy();
    const afterRevoke = await page.request.post("/mcp", {
      data: {
        jsonrpc: "2.0",
        id: 2,
        method: "tools/call",
        params: { name: "get_resource", arguments: { id: revision.id } },
      },
      headers: { Authorization: "Bearer " + author },
    });
    expect(afterRevoke.status()).toBe(401);
    expect(errors).toEqual([]);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBeTruthy();
    if (screenshots)
      await page.screenshot({
        path: screenshots + "/" + scope + "-material-verified.png",
        fullPage: true,
        animations: "disabled",
      });
  });
}
