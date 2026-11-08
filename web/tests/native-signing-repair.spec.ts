import { test, expect } from "./fixtures";
import { join } from "node:path";

// Real API/workspace/browser state, synthetic native packages and receipts.
// Separate Go Worker tests execute the actual fixed repair and QA processes.
for (const target of ["ios-appstore", "macos", "windows"]) {
  for (const mode of target === "macos"
    ? ["closed", "blocked", "notary-wait"]
    : ["closed", "blocked"]) {
    test(`${target} signed repair ${mode} exposes exact governed continuation`, async ({
      page,
      ownerSession,
    }, info) => {
      const errors: string[] = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const origin = "http://127.0.0.1:18082";
      const command = async (name: string, data: unknown, token?: string) => {
        const response = await page.request.post("/api/v1/commands/" + name, {
          data,
          headers: token
            ? { Authorization: "Bearer " + token }
            : { Origin: origin, "X-CSRF-Token": ownerSession.csrf },
        });
        expect(response.ok(), await response.text()).toBeTruthy();
        return response.json();
      };
      const project = await command("projects.create", {
        title: `${target} 修复 ${mode} ${info.project.name}`,
      });
      const goal = await command("goals.create", {
        projectId: project.id,
        title: "签名包独立质量修复",
        outcome: "按批准身份重新签名后独立验证",
        targets: [target],
        criteria: [{ id: "flow", text: "业务流程可用", method: "automated" }],
        budget: 100,
      });
      await command("goals.approve", { id: goal.id });
      const repo = await command("repositories.create", {
        projectId: project.id,
        title: "原生修复源码",
      });
      let commit = (
        await command("repositories.commit", {
          id: repo.id,
          branch: "seed",
          message: "Synthetic browser protocol source",
          files: { "source.txt": "broken protocol fixture" },
        })
      ).data.lastCommit;
      const runtime = target === "windows" ? "windows" : "darwin";
      const identity =
        target === "windows"
          ? {
              expectedWindowsPublisher: "CN=Company",
              expectedWindowsPackage: "com.company.feedback",
              expectedWindowsVersion: "1.0.0.0",
            }
          : target === "macos"
            ? {
                expectedApplicationSigner: "d".repeat(64),
                expectedTeamId: "ABCD123456",
              }
            : {
                expectedTeamId: "ABCD123456",
                expectedProvisioningProfile: "e".repeat(64),
              };
      const plan = await command("plans.propose", {
        goalId: goal.id,
        summary: "代码、无签名构建和专用签名",
        tasks: [
          {
            id: "code",
            title: "实现与修复",
            kind: "code",
            role: "developer",
            runtime,
            repositoryId: repo.id,
            commit,
            branch: "implementation",
            command: ["true"],
            budgetCents: 100,
          },
          {
            id: "unsigned",
            title: "无签名构建",
            kind: "build",
            role: "developer",
            runtime,
            repositoryId: repo.id,
            dependsOn: ["code"],
            target,
            artifactStage: "unsigned",
            command: ["true"],
            outputPath: "unsigned.bin",
            budgetCents: 100,
          },
          {
            id: "sign",
            title: "受控签名",
            kind: "build",
            role: "release",
            runtime,
            dependsOn: ["unsigned"],
            target,
            signingMode:
              target === "windows"
                ? "windows-msix"
                : target === "macos"
                  ? "macos-pkg"
                  : "ios-ipa",
            inputArtifactFrom: "unsigned",
            expectedSigner: "a".repeat(64),
            ...identity,
            budgetCents: 100,
          },
        ],
      });
      await command("plans.approve", { id: plan.id });
      const pipeline = await command("pipelines.create", {
        projectId: project.id,
        title: "最终包独立验证",
        runtime,
        method: "automated",
        verificationEnvironment: "ci",
        command: ["true"],
      });
      const policy = await command("quality.bind", {
        goalId: goal.id,
        expectedRequirementRevision: goal.revision,
        expectedPlanId: plan.id,
        pipelineId: pipeline.id,
        target,
        criteria: ["flow"],
        budgetCents: 100,
        key: "native-repair",
      });
      const worker = async (role: string, capabilities: string[]) => {
        const node = await command("workers.register", {
          projectId: project.id,
          title: role,
          runtime,
          roles: [role],
          capabilities,
          trustedQuality: role === "quality",
        });
        const response = await page.request.post("/api/v1/tokens", {
          data: { projectId: project.id, workerId: node.id },
          headers: { Origin: origin, "X-CSRF-Token": ownerSession.csrf },
        });
        expect(response.ok()).toBeTruthy();
        return (await response.json()).token as string;
      };
      const dev = await worker("developer", ["code", "build"]),
        sign = await worker("release", ["build"]),
        qa = await worker("quality", ["test"]);
      const finish = async (
        run: any,
        result: unknown,
        token: string,
        exitCode = 0,
      ) =>
        command(
          "tasks.complete",
          {
            id: run.id,
            leaseToken: run.data.leaseToken,
            exitCode,
            result,
            log: exitCode ? "关键业务流程未通过" : "",
          },
          token,
        );
      const code = async () => {
        const run = await command("tasks.claim", {}, dev),
          task = run.data.task,
          source = task.sourceBinding;
        commit = (
          await command(
            "repositories.commit",
            {
              id: repo.id,
              branch: task.branch,
              baseCommit: source.commit,
              expectedHead: source.expectedHead,
              runId: run.id,
              leaseToken: run.data.leaseToken,
              message: "Browser protocol implementation or repair",
              files: { "source.txt": run.id },
            },
            dev,
          )
        ).data.lastCommit;
        await finish(
          run,
          { repositoryId: repo.id, commit, branch: task.branch },
          dev,
        );
      };
      const upload = async (run: any, type: string, token: string) => {
        const query = new URLSearchParams({
          goalId: goal.id,
          repositoryId: repo.id,
          commit,
          target,
          type,
          name: "synthetic.bin",
          runId: run.id,
          leaseToken: run.data.leaseToken,
        });
        const response = await page.request.post(
          "/api/v1/artifacts/upload?" + query,
          {
            data: Buffer.from(type + commit),
            headers: {
              Authorization: "Bearer " + token,
              "Content-Type": "application/octet-stream",
            },
          },
        );
        expect(response.ok(), await response.text()).toBeTruthy();
        return response.json();
      };
      const unsigned = async () => {
        const run = await command("tasks.claim", {}, dev),
          artifact = await upload(run, "unsigned-package", dev);
        await finish(
          run,
          { repositoryId: repo.id, commit, artifactId: artifact.id },
          dev,
        );
        expect((await command("tasks.claim", {}, qa)).kind).toBe("worker");
      };
      const signature = async (wait = false) => {
        const run = await command("tasks.claim", {}, sign),
          input = run.data.task.signingInput;
        const result: Record<string, unknown> = {
          repositoryId: repo.id,
          commit,
          signingInputDigest: input.digest,
          operationKey: input.operationKey,
          certificateSHA256: input.expectedSigner,
          requestBinding: "c".repeat(64),
          payloadDigest: "b".repeat(64),
        };
        if (target === "macos")
          Object.assign(result, {
            applicationCertificateSHA256: input.expectedApplicationSigner,
            teamId: input.expectedTeamId,
            notarizationId: "11111111-2222-4333-8444-555555555555",
            ...(wait ? { signingPending: true } : { notarized: true }),
          });
        else if (target === "ios-appstore")
          Object.assign(result, {
            profileSHA256: input.expectedProvisioningProfile,
            teamId: input.expectedTeamId,
          });
        else
          Object.assign(result, {
            publisher: input.expectedWindowsPublisher,
            packageName: input.expectedWindowsPackage,
            version: input.expectedWindowsVersion,
            architecture: "x64",
            certificateSHA1: "A".repeat(40),
          });
        const artifact = wait ? null : await upload(run, "package", sign);
        if (artifact) result.artifactId = artifact.id;
        await finish(run, result, sign);
        return artifact;
      };
      await code();
      await unsigned();
      await signature();
      const failed = await command("tasks.claim", {}, qa);
      await finish(failed, {}, qa, 7);
      await page.goto("/");
      if (info.project.name === "mobile")
        await page
          .getByRole("button", { name: "打开导航", exact: true })
          .click();
      await page.getByLabel("选择项目").selectOption(project.id);
      await page
        .getByRole("navigation", { name: "主导航" })
        .getByRole("button", { name: /^质量与证据/ })
        .click();
      await page.getByLabel("选择交付目标").selectOption(goal.id);
      const dialog = page.getByRole("dialog");
      const show = async (stage: string) => {
        if (await dialog.isVisible()) await page.keyboard.press("Escape");
        await page
          .getByRole("button", { name: "刷新工作空间", exact: true })
          .click();
        await page
          .locator("button.resource-row")
          .filter({ hasText: "验证失败：" })
          .click();
        const detail = dialog.getByRole("region", { name: "签名包修复" });
        await expect(detail).toBeVisible();
        await expect(detail.getByRole("status")).toHaveText(stage);
        await expect(detail).toContainText("第 1 轮 / 最多 2 轮");
        return detail;
      };
      await show("源码修复");
      if (mode === "blocked") {
        await command("quality.revoke", {
          id: policy.id,
          reason: "撤销修复授权",
          key: "withdraw",
        });
        expect((await command("tasks.claim", {}, dev)).kind).toBe("worker");
        const detail = await show("源码修复");
        await expect(detail.getByText("需处理", { exact: true })).toBeVisible();
        await expect(detail).toContainText("质量");
      } else {
        await code();
        await show("无签名重建");
        await unsigned();
        await show("受控签名");
        const artifact = await signature(mode === "notary-wait");
        if (mode === "notary-wait") {
          await show("等待 Apple 公证结果");
          expect((await command("tasks.claim", {}, qa)).kind).toBe("worker");
        } else {
          await show("签后独立复验");
          const verify = await command("tasks.claim", {}, qa);
          expect(verify.data.task.artifactId).toBe(artifact.id);
          expect(verify.data.task.qualityPolicyId).toBe(policy.id);
          await finish(verify, {}, qa);
          await show("独立复验通过，缺陷已关闭");
        }
      }
      const detail = dialog.getByRole("region", { name: "签名包修复" });
      await expect(detail).toContainText("沿用批准的签名身份和质量策略");
      await expect(detail).toContainText("正式交付仍需目标环境验收和发布批准");
      if (process.env.IS_QUALITY_SCREENSHOTS) {
        await detail.scrollIntoViewIfNeeded();
        await page.screenshot({
          path: join(
            process.env.IS_QUALITY_SCREENSHOTS,
            `${target}-${mode}.png`,
          ),
          animations: "disabled",
        });
      }
      expect(errors).toEqual([]);
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBeTruthy();
    });
  }
}
