import { test, expect } from "./fixtures";
import { Buffer } from "node:buffer";
import { join } from "node:path";

for (const mode of ["complete", "blocked"]) {
  test(`Windows signing ${mode} preserves approved package identity and native acceptance`, async ({
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
      title: `Windows 签名 ${mode} ${info.project.name}`,
    });
    const goal = await command("goals.create", {
      projectId: project.id,
      title: "Windows 应用可信签名",
      outcome: "准确签名后独立验收",
      targets: ["windows"],
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
      message: "Explicitly synthetic Windows fixture",
      files: { "source.txt": "unsigned Windows input" },
    });
    const commit = source.data.lastCommit;
    const plan = await command("plans.propose", {
      goalId: goal.id,
      summary: "原生构建后由专用节点签名",
      tasks: [
        {
          id: "unsigned",
          title: "无签名 Windows 安装包",
          kind: "build",
          role: "developer",
          runtime: "windows",
          repositoryId: repo.id,
          commit,
          target: "windows",
          artifactStage: "unsigned",
          command: ["true"],
          outputPath: "unsigned.msix",
        },
        {
          id: "sign",
          title: "Windows MSIX 签名",
          kind: "build",
          role: "release",
          runtime: "windows",
          target: "windows",
          signingMode: "windows-msix",
          inputArtifactFrom: "unsigned",
          dependsOn: ["unsigned"],
          expectedSigner: "a".repeat(64),
          expectedWindowsPublisher: "CN=Company",
          expectedWindowsPackage: "com.company.feedback",
          expectedWindowsVersion: "1.0.0.0",
        },
      ],
    });
    await command("plans.approve", { id: plan.id });
    const worker = async (role: string) => {
      const node = await command("workers.register", {
        projectId: project.id,
        title: role + " 原生节点",
        runtime: "windows",
        roles: [role],
        capabilities: ["build"],
      });
      const response = await page.request.post("/api/v1/tokens", {
        data: { projectId: project.id, workerId: node.id },
        headers: {
          Origin: "http://127.0.0.1:18082",
          "X-CSRF-Token": ownerSession.csrf,
        },
      });
      expect(response.ok()).toBeTruthy();
      return (await response.json()).token as string;
    };
    const builder = await worker("developer"),
      signer = await worker("release");
    const upload = async (
      run: any,
      type: string,
      bytes: string,
      token: string,
    ) => {
      const query = new URLSearchParams({
        goalId: goal.id,
        repositoryId: repo.id,
        commit,
        target: "windows",
        type,
        name: type === "package" ? "synthetic.msix" : "unsigned.msix",
        runId: run.id,
        leaseToken: run.data.leaseToken,
      });
      const response = await page.request.post(
        "/api/v1/artifacts/upload?" + query,
        {
          data: Buffer.from(bytes),
          headers: {
            Authorization: "Bearer " + token,
            "Content-Type": "application/octet-stream",
          },
        },
      );
      expect(response.ok(), await response.text()).toBeTruthy();
      return response.json();
    };
    const built = await command("tasks.claim", {}, builder);
    const unsigned = await upload(
      built,
      "unsigned-package",
      "unsigned Windows input",
      builder,
    );
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
    const artifact =
      mode === "complete"
        ? await upload(
            run,
            "package",
            "explicitly synthetic signed MSIX",
            signer,
          )
        : null;
    await command(
      "tasks.complete",
      {
        id: run.id,
        leaseToken: run.data.leaseToken,
        exitCode: mode === "complete" ? 0 : 1,
        result: artifact
          ? {
              artifactId: artifact.id,
              repositoryId: repo.id,
              commit,
              signingInputDigest: input.digest,
              operationKey: input.operationKey,
              certificateSHA256: "a".repeat(64),
              publisher: "CN=Company",
              packageName: "com.company.feedback",
              version: "1.0.0.0",
              architecture: "x64",
              certificateSHA1: "A".repeat(40),
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
        name: "查看工作项：Windows MSIX 签名",
        exact: true,
      })
      .click();
    const dialog = page.getByRole("dialog");
    const region = dialog.getByRole("region", {
      name: "Windows MSIX 签名",
      exact: true,
    });
    await expect(region.getByText("CN=Company", { exact: true })).toBeVisible();
    await expect(
      region.getByText("com.company.feedback", { exact: true }),
    ).toBeVisible();
    await expect(region.getByText("1.0.0.0", { exact: true })).toBeVisible();
    await expect(
      region.getByText(
        mode === "complete"
          ? "签名 MSIX 已生成，独立验证结果见质量中心。"
          : "签名结果需要核对，重新签名前需批准新的方案。",
        { exact: true },
      ),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "重试工作项", exact: true }),
    ).toHaveCount(0);
    await page.addStyleTag({
      content:
        "*,*::before,*::after{animation:none!important;transition:none!important;}",
    });
    await region.scrollIntoViewIfNeeded();
    const width = await region.evaluate((el) => ({
      scroll: el.scrollWidth,
      client: el.clientWidth,
    }));
    expect(width.scroll).toBeLessThanOrEqual(width.client + 1);
    if (process.env.IS_QUALITY_SCREENSHOTS)
      await page.screenshot({
        path: join(process.env.IS_QUALITY_SCREENSHOTS, `windows-${mode}.png`),
        fullPage: true,
      });
    const acceptance = region.getByText(
      "正式交付仍需 Windows 安装与业务验证、发布批准和公开分发验收。",
      { exact: true },
    );
    await acceptance.scrollIntoViewIfNeeded();
    await expect(acceptance).toBeInViewport();
    if (process.env.IS_QUALITY_SCREENSHOTS)
      await page.screenshot({
        path: join(
          process.env.IS_QUALITY_SCREENSHOTS,
          `windows-${mode}-acceptance.png`,
        ),
        fullPage: true,
      });
  });
}
