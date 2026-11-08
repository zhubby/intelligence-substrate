import { date, type Resource } from "./api";
import { useWorkspace } from "./context";
import { Button, Confirm, Form } from "./ui";

const labels: Record<string, string> = {
  awaiting_approval: "待独立批准",
  approved: "资料已批准",
  dispatched: "已派发资料修订",
  verified: "资料发布已验证",
  stale: "需求已变化",
};

export function StoreRevisions({
  release,
  issue,
}: {
  release: Resource;
  issue: Resource;
}) {
  const w = useWorkspace();
  const revisions = w
    .select("store_revision")
    .filter((x) => x.data.spec.issueId === issue.id);
  const target = release.data.targets.find(
    (x: any) => x.target === "ios-appstore",
  );
  const active =
    ["store-text", "store-media"].includes(issue.data.triage?.strategy) &&
    issue.status !== "resolved" &&
    target?.rejectionIssueId === issue.id &&
    target?.status === "rejected";
  if (!active && !revisions.length) return null;
  const screenshots = issue.data.triage?.strategy === "store-media";
  const canRelease = w.actor.kind === "human" && w.can("release");
  const materials = w
    .select("artifact")
    .filter(
      (x) =>
        x.goalId === release.goalId &&
        x.data.type === "report" &&
        x.data.target === "ios-appstore" &&
        x.data.requirementRevision === release.data.requirementRevision &&
        x.data.commit === release.data.commit &&
        x.data.digest !== issue.data.source.metadataDigest,
    );
  return (
    <section
      className="store-revisions"
      aria-label={"商店资料修订 " + issue.id}
    >
      <h5>
        {screenshots ? "App Store 截图与资料修订" : "App Store 文字资料修订"}
      </h5>
      <p className="muted">
        {screenshots
          ? "保留原构建，修订已批准截图、文字与链接。新截图处理期间保留原展示项；全部就绪后替换并重新提审。"
          : "保留原构建与截图，仅修订文字和链接。"}
        新清单需独立批准；审核通过并完成对应资料的独立发布验收后，问题才会解决。
      </p>
      {active && (
        <Button
          secondary
          disabled={!w.can("developer", "release")}
          onClick={() => {
            const key = crypto.randomUUID();
            w.open(
              screenshots ? "提交截图与资料修订" : "提交文字资料修订",
              <Form
                fields={[
                  {
                    name: "metadataArtifactId",
                    label: "新商店资料清单",
                    type: "select",
                    required: true,
                    options: Object.fromEntries(
                      materials.map((x) => [x.id, x.title]),
                    ),
                  },
                ]}
                submit="提交独立审批"
                onSubmit={async (input) => {
                  await w.run("store_revisions.propose", {
                    issueId: issue.id,
                    metadataArtifactId: input.metadataArtifactId,
                    key,
                  });
                  w.close();
                }}
              >
                <p className="form-note">
                  选择同提交、同渠道的 JSON 报告。
                  {screenshots
                    ? "语言与原构建保持，清单中的截图字节、规格、数量及顺序纳入本次批准。"
                    : "发布节点会检查 locale、截图字节和顺序未改变。"}
                  提案人和资料作者不能自批。
                </p>
              </Form>,
            );
          }}
        >
          提交资料修订
        </Button>
      )}
      {revisions.map((rev) => {
        const spec = rev.data.spec;
        const material = w.items.find((x) => x.id === spec.metadataArtifactId);
        const source = w.items.find((x) => x.id === spec.artifactId);
        return (
          <article
            className="release-issue"
            key={rev.id}
            aria-label={"资料修订 " + rev.id}
          >
            <div className="content-heading">
              <strong>{material?.title || "商店资料清单"}</strong>
              <span className={"badge status-" + rev.status}>
                <span />
                {labels[rev.status] || rev.status}
              </span>
            </div>
            <p className="muted">
              {date(rev.createdAt)} · 原构建 {source?.title || spec.artifactId}
            </p>
            <a
              className="text-button"
              href={"/api/v1/artifacts/" + spec.metadataArtifactId + "/content"}
              target="_blank"
              rel="noopener noreferrer"
            >
              查看新资料清单
            </a>
            <details className="source-details">
              <summary>批准与资料来源</summary>
              <dl>
                <div>
                  <dt>批准范围</dt>
                  <dd>
                    {spec.materialScope === "screenshots"
                      ? "截图、文字与链接"
                      : "仅文字与链接，截图保持"}
                  </dd>
                </div>
                <div>
                  <dt>原资料摘要</dt>
                  <dd className="mono">{spec.sourceMetadataDigest}</dd>
                </div>
                <div>
                  <dt>新资料摘要</dt>
                  <dd className="mono">{spec.metadataDigest}</dd>
                </div>
                <div>
                  <dt>资料批准绑定</dt>
                  <dd className="mono">{rev.data.binding}</dd>
                </div>
                <div>
                  <dt>原外部版本</dt>
                  <dd className="mono">{spec.externalId}</dd>
                </div>
              </dl>
            </details>
            {rev.status === "awaiting_approval" && (
              <Button
                secondary
                disabled={
                  !canRelease ||
                  !active ||
                  spec.createdBy === w.actor.id ||
                  material?.data.producerId === w.actor.id
                }
                onClick={() =>
                  w.open(
                    "批准商店资料修订",
                    <Confirm
                      description={
                        <p>
                          {spec.materialScope === "screenshots"
                            ? "批准这份截图、文字与链接清单，绑定原构建和拒审修复要求。新图片就绪后才替换展示项。"
                            : "批准这份文字与链接清单，绑定原构建和拒审修复要求。"}
                          批准后仍需发布节点执行并等待渠道审核。
                        </p>
                      }
                      onConfirm={async () => {
                        await w.run("store_revisions.approve", {
                          id: rev.id,
                          expectedBinding: rev.data.binding,
                        });
                        w.close();
                      }}
                    />,
                  )
                }
              >
                审阅并批准资料
              </Button>
            )}
            {rev.status === "approved" && (
              <Button
                disabled={!canRelease || !active}
                onClick={() => {
                  const key = crypto.randomUUID();
                  w.open(
                    "执行商店资料修订",
                    <Confirm
                      label="确认执行"
                      description={
                        <p>
                          {spec.materialScope === "screenshots"
                            ? "由原发布节点上传已批准截图，持久等待处理就绪后替换展示项并重新提审。"
                            : "由原发布节点修订已批准文字并重新提审。"}
                          使用原构建和审核项，保留其他渠道状态。
                        </p>
                      }
                      onConfirm={async () => {
                        await w.run("store_revisions.dispatch", {
                          id: rev.id,
                          workerId: target.publisherWorkerId,
                          key,
                        });
                        w.close();
                      }}
                    />,
                  );
                }}
              >
                执行资料修订
              </Button>
            )}
            {rev.status === "dispatched" && (
              <p className="muted">
                当前渠道状态：
                {target?.materialRevisionId !== rev.id
                  ? "已有后续资料修订，等待当前资料的独立发布验收"
                  : target?.status === "rejected"
                    ? "等待发布节点领取"
                    : target?.status === "revising_materials"
                      ? "正在修订并重新提审"
                      : target?.status === "processing" &&
                          spec.materialScope === "screenshots"
                        ? "等待新截图处理，尚未重新提审"
                        : target?.status === "reconciliation_required"
                          ? "外部结果待核对，不能直接重试"
                          : target?.status === "awaiting_review"
                            ? "等待渠道审核"
                            : target?.status === "published"
                              ? "已发布，等待独立验收"
                              : "请查看渠道执行记录"}
              </p>
            )}
          </article>
        );
      })}
    </section>
  );
}
