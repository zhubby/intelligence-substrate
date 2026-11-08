import { date, targets, type Data, type Resource } from "./api";
import { useWorkspace } from "./context";
import { Button, Form } from "./ui";
import { StoreRevisions } from "./StoreRevisions";

const labels: Record<string, string> = {
  open: "待确认修复要求",
  triaged: "待重新规划",
  planned: "修复交付中",
  resolved: "修复已验证",
};

export function currentReleaseIssues(items: Resource[], goal: Resource) {
  return items.filter(
    (item) =>
      item.kind === "release_issue" &&
      item.goalId === goal.id &&
      item.status !== "resolved" &&
      goal.data.targets.includes(item.data.source.target),
  );
}

export function FrozenReleaseIssues({ entries }: { entries?: Data[] }) {
  if (!entries?.length) return null;
  return (
    <details className="source-details release-issue-snapshot">
      <summary>关联拒审修复（{entries.length}）</summary>
      {entries.map((entry) => (
        <div key={entry.id}>
          <p>
            <strong>{targets[entry.source.target]}</strong> · 原提交{" "}
            <code>{entry.source.commit.slice(0, 12)}</code>
          </p>
          <p>拒审原因：{entry.source.reason || "待核查渠道说明"}</p>
          <p>已确认修复要求：{entry.triage.instructions}</p>
          <p>修复后重新构建、独立验证并申请发布批准。</p>
        </div>
      ))}
    </details>
  );
}

export function ReleaseIssues({ release }: { release: Resource }) {
  const w = useWorkspace();
  const issues = w
    .select("release_issue")
    .filter((issue) => issue.data.source.releaseId === release.id);
  if (!issues.length) return null;
  return (
    <section className="release-issues" aria-label={"拒审问题 " + release.id}>
      <h4>拒审问题与修复</h4>
      {issues.map((issue) => {
        const { source, triage, resolution } = issue.data;
        const goal = w.items.find((item) => item.id === issue.goalId);
        const inScope = goal?.data.targets.includes(source.target);
        const replacement = w.items.find(
          (item) => item.id === resolution?.releaseId,
        );
        const needsConfirmation =
          issue.status === "open" ||
          triage?.requirementRevision !== goal?.revision;
        return (
          <article className="release-issue" key={issue.id}>
            <div className="content-heading">
              <strong>{targets[source.target]}</strong>
              <span className={"badge status-" + issue.status}>
                <span />
                {needsConfirmation && issue.status !== "resolved"
                  ? labels.open
                  : issue.status === "triaged" &&
                      ["store-text", "store-media"].includes(triage?.strategy)
                    ? "待资料修订"
                    : labels[issue.status] || issue.status}
              </span>
            </div>
            <p>拒审原因：{source.reason || "请发布负责人核查渠道说明"}</p>
            <p className="muted">
              原版本 {release.title} · 需求 v{source.requirementRevision} ·{" "}
              {date(source.observation.at)}
            </p>
            <p className="mono">
              {source.externalId || "渠道未提供外部版本标识"}
            </p>
            {triage && <p>修复要求：{triage.instructions}</p>}
            {resolution ? (
              <p>
                版本 {replacement?.title || resolution.releaseId}{" "}
                已通过该渠道的独立发布验收。
              </p>
            ) : !inScope ? (
              <p className="muted">
                此渠道已移出当前交付范围，问题保留为历史记录。
              </p>
            ) : (
              <>
                <p className="muted">
                  新构建修复需要重新规划、独立验证与发布批准。App Store
                  文字或截图资料修订可保留原构建，单独提交清单审批。该渠道独立发布验收通过后，问题才会解决。
                </p>
                <Button
                  secondary
                  disabled={
                    w.actor.kind !== "human" ||
                    !w.can("product", "architect", "release") ||
                    goal?.data.approvedRevision !== goal?.revision
                  }
                  onClick={() => {
                    const key = crypto.randomUUID();
                    w.open(
                      "确认拒审修复要求",
                      <Form
                        fields={[
                          ...(source.target === "ios-appstore" &&
                          source.operationKey &&
                          source.metadataDigest
                            ? [
                                {
                                  name: "strategy",
                                  label: "修复方式",
                                  type: "select" as const,
                                  options: {
                                    "new-build": "修改代码并重新构建",
                                    "store-text": "仅修订 App Store 文字与链接",
                                    "store-media": "修订 App Store 截图与资料",
                                  },
                                  defaultValue: triage?.strategy || "new-build",
                                },
                              ]
                            : []),
                          {
                            name: "instructions",
                            label: "修复要求",
                            type: "textarea",
                            defaultValue: triage?.instructions || "",
                            required: true,
                          },
                        ]}
                        submit={
                          source.target === "ios-appstore" &&
                          source.operationKey &&
                          source.metadataDigest
                            ? "确认修复方式"
                            : "确认并纳入规划"
                        }
                        onSubmit={async (input) => {
                          await w.run("release_issues.triage", {
                            id: issue.id,
                            expectedRevision: issue.revision,
                            expectedBinding: issue.data.binding,
                            key,
                            strategy: input.strategy || "new-build",
                            instructions: input.instructions,
                          });
                          w.close();
                        }}
                      >
                        <p className="form-note">
                          代码修复纳入后续方案，文字或截图资料修订进入单独审批；调整已确认要求时，相关方案、候选或资料需要重新确认。
                        </p>
                      </Form>,
                    );
                  }}
                >
                  {needsConfirmation ? "确认修复要求" : "调整修复要求"}
                </Button>
              </>
            )}
            <StoreRevisions release={release} issue={issue} />
            <details className="source-details">
              <summary>来源与验收追溯</summary>
              <dl>
                <div>
                  <dt>原提交</dt>
                  <dd className="mono">{source.commit}</dd>
                </div>
                <div>
                  <dt>原制品</dt>
                  <dd className="mono">{source.artifactDigest}</dd>
                </div>
                <div>
                  <dt>问题记录</dt>
                  <dd className="mono">{issue.id}</dd>
                </div>
                {issue.data.planId && (
                  <div>
                    <dt>修复方案</dt>
                    <dd className="mono">{issue.data.planId}</dd>
                  </div>
                )}
                {resolution && (
                  <div>
                    <dt>独立验收</dt>
                    <dd className="mono">{resolution.evidenceId}</dd>
                  </div>
                )}
              </dl>
            </details>
          </article>
        );
      })}
    </section>
  );
}
