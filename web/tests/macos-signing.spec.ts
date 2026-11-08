import { test, expect } from "./fixtures";
import { Buffer } from "node:buffer";
import { join } from "node:path";

for (const mode of ["pending", "blocked"]) {
  test(`macOS signing ${mode} preserves its exact operation and delivery boundary`, async ({
    page,
    ownerSession,
  }, info) => {
    const command = async (name: string, data: unknown, token?: string) => {
      const response = await page.request.post("/api/v1/commands/" + name, {
        data,
        headers: token
          ? { Authorization: "Bearer " + token }
          : {
              Origin: "http://127.0.0.1:18082",
              "X-CSRF-Token": ownerSession.csrf,
            },
      });
      expect(response.ok(), await response.text()).toBeTruthy();
      return response.json();
    };
    const project = await command("projects.create", {
      title: `macOS 公证 ${mode} ${info.project.name}`,
    });
    const goal = await command("goals.create", {
      projectId: project.id,
      title: "桌面应用可信签名",
      outcome: "签名不等于正式交付",
      targets: ["macos"],
      criteria: [{ id: "flow", text: "业务流程可用", method: "automated" }],
      budget: 100,
    });
    await command("goals.approve", { id: goal.id });
    const repo = await command("repositories.create", {
      projectId: project.id,
      title: "原生源码",
    });
    const source = await command("repositories.commit", {
      id: repo.id,
      branch: "feature",
      message: "Explicit synthetic native fixture",
      files: { "source.txt": "synthetic unsigned input" },
    });
    const commit = source.data.lastCommit;
    const plan = await command("plans.propose", {
      goalId: goal.id,
      summary: "构建后在专用节点签名",
      tasks: [
        {
          id: "unsigned",
          title: "无签名应用构建",
          kind: "build",
          role: "developer",
          runtime: "darwin",
          repositoryId: repo.id,
          commit,
          target: "macos",
          artifactStage: "unsigned",
          command: ["true"],
          outputPath: "unsigned.app.tar.gz",
          budgetCents: 100,
        },
        {
          id: "sign",
          title: "macOS 签名与公证",
          kind: "build",
          role: "release",
          runtime: "darwin",
          target: "macos",
          signingMode: "macos-pkg",
          inputArtifactFrom: "unsigned",
          dependsOn: ["unsigned"],
          expectedSigner: "a".repeat(64),
          expectedApplicationSigner: "d".repeat(64),
          expectedTeamId: "ABCD123456",
          budgetCents: 100,
        },
      ],
    });
    await command("plans.approve", { id: plan.id });
    const worker = async (role: string) => {
      const node = await command("workers.register", {
        projectId: project.id,
        title: role + " 原生节点",
        runtime: "darwin",
        roles: [role],
        capabilities: ["build"],
      });
      const issued = await page.request.post("/api/v1/tokens", {
        data: { projectId: project.id, workerId: node.id },
        headers: {
          Origin: "http://127.0.0.1:18082",
          "X-CSRF-Token": ownerSession.csrf,
        },
      });
      expect(issued.ok()).toBeTruthy();
      return (await issued.json()).token as string;
    };
    const builder = await worker("developer"),
      signer = await worker("release");
    const built = await command("tasks.claim", {}, builder);
    const query = new URLSearchParams({
      goalId: goal.id,
      repositoryId: repo.id,
      commit,
      target: "macos",
      type: "unsigned-package",
      name: "synthetic.app.tar.gz",
      runId: built.id,
      leaseToken: built.data.leaseToken,
    });
    const uploaded = await page.request.post(
      "/api/v1/artifacts/upload?" + query,
      {
        data: Buffer.from("synthetic unsigned input"),
        headers: {
          Authorization: "Bearer " + builder,
          "Content-Type": "application/octet-stream",
        },
      },
    );
    expect(uploaded.ok(), await uploaded.text()).toBeTruthy();
    const unsigned = await uploaded.json();
    await command(
      "tasks.complete",
      {
        id: built.id,
        leaseToken: built.data.leaseToken,
        exitCode: 0,
        result: { artifactId: unsigned.id, repositoryId: repo.id, commit },
      },
      builder,
    );
    const run = await command("tasks.claim", {}, signer);
    const input = run.data.task.signingInput;
    await command(
      "tasks.complete",
      {
        id: run.id,
        leaseToken: run.data.leaseToken,
        exitCode: mode === "pending" ? 0 : 1,
        result:
          mode === "pending"
            ? {
                signingPending: true,
                repositoryId: repo.id,
                commit,
                signingInputDigest: input.digest,
                operationKey: input.operationKey,
                certificateSHA256: "a".repeat(64),
                applicationCertificateSHA256: "d".repeat(64),
                teamId: "ABCD123456",
                notarizationId: "11111111-2222-4333-8444-555555555555",
                payloadDigest: "b".repeat(64),
                requestBinding: "c".repeat(64),
              }
            : { blocked: true, reason: "签名结果未确认，需要核对原操作" },
      },
      signer,
    );
    await page.goto("/");
    if (info.project.name === "mobile")
      await page.getByRole("button", { name: "打开导航", exact: true }).click();
    await page.getByLabel("选择项目").selectOption(project.id);
    await page
      .getByRole("navigation", { name: "主导航" })
      .getByRole("button", { name: /^目标与需求/ })
      .click();
    await page.getByLabel("选择交付目标").selectOption(goal.id);
    await page.getByRole("tab", { name: "执行记录", exact: true }).click();
    await page
      .getByRole("button", {
        name: "查看工作项：macOS 签名与公证",
        exact: true,
      })
      .click();
    const dialog = page.getByRole("dialog");
    const region = dialog.getByRole("region", {
      name: "macOS 签名与公证",
      exact: true,
    });
    await expect(region.getByText("ABCD123456", { exact: true })).toBeVisible();
    if (mode === "pending") {
      await expect(
        dialog.getByText("等待 Apple 公证", { exact: true }),
      ).toBeVisible();
      await expect(
        region.getByText("签名节点已释放，平台将查询原公证结果。", {
          exact: true,
        }),
      ).toBeVisible();
      await expect(
        region.getByText("11111111-2222-4333-8444-555555555555", {
          exact: true,
        }),
      ).toBeVisible();
      await expect(
        region.getByText("尚未生成最终签名安装包。", { exact: true }),
      ).toBeVisible();
    } else {
      await expect(
        region.getByText("签名结果需要核对，重新签名前需批准新的方案。", {
          exact: true,
        }),
      ).toBeVisible();
    }
    await expect(
      region.getByText("正式交付仍需独立质量验证和发布验收。", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "重试工作项", exact: true }),
    ).toHaveCount(0);
    await page.addStyleTag({
      content:
        "*,*::before,*::after{animation:none!important;transition:none!important;}",
    });
    await expect(dialog).toBeVisible();
    const width = await region.evaluate((el) => ({
      scroll: el.scrollWidth,
      client: el.clientWidth,
    }));
    expect(width.scroll).toBeLessThanOrEqual(width.client + 1);
    if (process.env.IS_QUALITY_SCREENSHOTS)
      await page.screenshot({
        path: join(process.env.IS_QUALITY_SCREENSHOTS, `macos-${mode}.png`),
        fullPage: true,
      });
  });
}
