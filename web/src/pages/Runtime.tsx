import { useEffect, useState } from "react";
import { Activity, ExternalLink, RotateCcw } from "lucide-react";
import { date, upload, type Resource } from "../api";
import { useWorkspace } from "../context";
import { Badge, Button, Confirm, Form } from "../ui";
import { RuntimeIncidents } from "./RuntimeIncidents";

export function RuntimePanel() {
  const w = useWorkspace();
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 15000);
    return () => clearInterval(timer);
  }, []);
  const deployments = w
    .select("deployment")
    .filter((x) => !w.goal || x.goalId === w.goal.id);
  const active = deployments.filter((x) => x.status === "active");
  if (!deployments.length) return null;
  const rollbacks = w
    .select("rollback")
    .filter((x) => !w.goal || x.goalId === w.goal.id);
  const incidents = w
    .select("incident")
    .filter((x) => !w.goal || x.goalId === w.goal.id);
  function reportOptions(d: Resource) {
    return Object.fromEntries(
      w
        .select("artifact")
        .filter(
          (x) =>
            x.goalId === d.goalId &&
            x.data.type === "report" &&
            x.data.commit === d.data.commit &&
            x.data.target === "web" &&
            x.createdAt >= d.data.activatedAt,
        )
        .map((x) => [x.id, x.title]),
    );
  }
  return (
    <section className="runtime-section" aria-label="应用运行">
      <div className="runtime-heading">
        <Activity size={20} />
        <div>
          <h3>应用运行</h3>
          <p>跟踪正在提供服务的版本。健康检查与独立业务验收分别记录。</p>
        </div>
      </div>
      {active.map((d) => {
        const health =
          !d.data.validUntil || Date.parse(d.data.validUntil) < now
            ? "unknown"
            : d.data.health;
        const history = deployments.filter(
          (x) =>
            x.status === "superseded" &&
            x.data.application === d.data.application &&
            x.data.workerId === d.data.workerId &&
            x.data.dataContract === d.data.dataContract,
        );
        const pending = rollbacks.some(
          (x) =>
            ["approved", "running", "reconciliation_required"].includes(
              x.status,
            ) && x.data.sourceId === d.id,
        );
        const publicationApprover =
          d.data.approvedBy === w.actor.id || d.data.activatedBy === w.actor.id;
        const packageArtifact = w
          .select("artifact")
          .find((x) => x.id === d.data.artifactId);
        const producer = packageArtifact?.data.producerId === w.actor.id;
        return (
          <article
            className="runtime-card"
            key={d.id}
            aria-label={"运行部署 " + d.id}
          >
            <div className="runtime-version">
              <div>
                <span className="eyebrow">CURRENT APPLICATION</span>
                <h4>{d.title}</h4>
                <p className="mono runtime-id">{d.data.externalId}</p>
              </div>
              <Badge status={health} />
            </div>
            <dl className="runtime-facts">
              <div>
                <dt>最近检查</dt>
                <dd>{date(d.data.lastCheckedAt) || "尚无检查"}</dd>
              </div>
              <div>
                <dt>响应耗时</dt>
                <dd>{d.data.lastCheckedAt ? `${d.data.latencyMs} ms` : "—"}</dd>
              </div>
              <div>
                <dt>独立运行验收</dt>
                <dd>
                  {d.data.verificationStatus === "verified"
                    ? "运行验收已通过"
                    : "运行验收待完成"}
                </dd>
              </div>
            </dl>
            <div className="runtime-actions">
              {d.data.url && (
                <a
                  className="text-button"
                  href={d.data.url}
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  打开应用 <ExternalLink size={13} />
                </a>
              )}
              <Button
                secondary
                disabled={!w.can("quality", "release") || !packageArtifact}
                onClick={() =>
                  w.open(
                    "上传运行报告",
                    <Form
                      fields={[
                        {
                          name: "file",
                          label: "实际运行报告文件",
                          type: "file",
                          required: true,
                        },
                      ]}
                      submit="上传运行报告"
                      onSubmit={async (v) => {
                        const file = v.file as File;
                        await upload(
                          {
                            goalId: d.goalId,
                            deploymentId: d.id,
                            repositoryId: packageArtifact?.data.repositoryId,
                            commit: d.data.commit,
                            target: "web",
                            type: "report",
                            name: file.name,
                          },
                          file,
                        );
                        await w.refresh();
                        w.close();
                      }}
                    >
                      <p className="form-note">
                        报告将关联当前运行版本。请记录实际业务检查、数据兼容性与观察结果。
                      </p>
                    </Form>,
                  )
                }
              >
                上传运行报告
              </Button>
              <Button
                secondary
                disabled={
                  !w.can("release", "developer", "architect") ||
                  !history.length ||
                  pending
                }
                onClick={() =>
                  w.open(
                    "提出运行版本回滚",
                    <Form
                      fields={[
                        {
                          name: "destinationId",
                          label: "回滚目标",
                          type: "select",
                          required: true,
                          options: Object.fromEntries(
                            history.map((x) => [
                              x.id,
                              `${x.title} · ${x.data.commit.slice(0, 12)}`,
                            ]),
                          ),
                        },
                        {
                          name: "reason",
                          label: "回滚原因",
                          type: "textarea",
                          required: true,
                        },
                      ]}
                      submit="提交回滚建议"
                      onSubmit={async (v) => {
                        await w.run("runtime.rollback.propose", {
                          id: d.id,
                          ...v,
                          key: crypto.randomUUID(),
                        });
                        w.close();
                      }}
                    >
                      <p className="form-note">
                        回滚绑定当前运行版本与指定历史制品。提交建议后，由独立发布人批准执行。
                      </p>
                    </Form>,
                  )
                }
              >
                <RotateCcw size={14} />
                提出回滚
              </Button>
              {d.data.verificationStatus !== "verified" && (
                <Button
                  secondary
                  disabled={
                    !w.can("quality") || publicationApprover || producer
                  }
                  onClick={() =>
                    w.open(
                      "独立运行验收",
                      <Form
                        fields={[
                          {
                            name: "proofArtifactId",
                            label: "实际运行报告",
                            type: "select",
                            required: true,
                            options: reportOptions(d),
                          },
                          {
                            name: "statement",
                            label: "独立运行验收记录",
                            type: "textarea",
                            required: true,
                          },
                        ]}
                        submit="记录独立运行验收"
                        onSubmit={async (v) => {
                          await w.run("runtime.verify", { id: d.id, ...v });
                          w.close();
                        }}
                      >
                        <p className="form-note">
                          请核对实际服务版本、关键业务及数据兼容性。此记录不能替代新需求的验收。
                        </p>
                      </Form>,
                    )
                  }
                >
                  记录运行验收
                </Button>
              )}
            </div>
            {health === "unknown" && (
              <p className="form-note">
                尚无有效的近期运行信号，请检查监测节点。
              </p>
            )}
          </article>
        );
      })}
      <RuntimeIncidents incidents={incidents} />
      {rollbacks.map((r) => {
        const source = deployments.find((x) => x.id === r.data.sourceId);
        const destination = deployments.find(
          (x) => x.id === r.data.destinationId,
        );
        const producer =
          w
            .select("artifact")
            .find((x) => x.id === destination?.data.artifactId)?.data
            .producerId === w.actor.id;
        return (
          <div className="runtime-rollback" key={r.id}>
            <div>
              <strong>{r.title}</strong>
              <p>{r.data.reason}</p>
              <small>
                {source?.title} → {destination?.title}
              </small>
              {r.status === "awaiting_approval" && (
                <p className="form-note">回滚建议等待独立发布人批准</p>
              )}
            </div>
            <div className="button-group">
              <Badge status={r.status} />
              {r.status === "awaiting_approval" && (
                <Button
                  secondary
                  disabled={
                    !w.can("release") || producer || source?.status !== "active"
                  }
                  onClick={() =>
                    w.open(
                      "审阅生产回滚",
                      <Confirm
                        label="批准本次回滚"
                        description={
                          <>
                            <p>
                              将正在运行的 {source?.title} 回滚至{" "}
                              {destination?.title}。
                            </p>
                            <p>{r.data.reason}</p>
                            <p>
                              平台会核对当前版本；执行后需要重新检查运行状态与独立验收。
                            </p>
                          </>
                        }
                        onConfirm={async () => {
                          await w.run("runtime.rollback.approve", { id: r.id });
                          w.close();
                        }}
                      />,
                    )
                  }
                >
                  审阅回滚
                </Button>
              )}
              {r.status === "reconciliation_required" && (
                <Button
                  secondary
                  disabled={!w.can("release")}
                  onClick={() =>
                    w.open(
                      "核对实际运行版本",
                      <Form
                        fields={[
                          {
                            name: "state",
                            label: "实际运行结论",
                            type: "select",
                            required: true,
                            options: {
                              succeeded: "目标历史制品已经重新激活",
                              failed: "原版本仍在运行，回滚未生效",
                            },
                          },
                          {
                            name: "proofArtifactId",
                            label: "实际核对报告",
                            type: "select",
                            required: true,
                            options: Object.fromEntries(
                              w
                                .select("artifact")
                                .filter(
                                  (x) =>
                                    x.goalId === r.goalId &&
                                    x.data.type === "report" &&
                                    x.data.target === "web" &&
                                    x.createdAt >= r.data.approvedAt,
                                )
                                .map((x) => [x.id, x.title]),
                            ),
                          },
                          {
                            name: "reason",
                            label: "核对结论",
                            type: "textarea",
                            required: true,
                          },
                        ]}
                        submit="记录核对结论"
                        onSubmit={async (v) => {
                          await w.run("runtime.rollback.reconcile", {
                            id: r.id,
                            ...v,
                          });
                          w.close();
                        }}
                      >
                        <p className="form-note">
                          先在运行节点核对并恢复明确版本，再附实际报告。结果不明时不能重新执行回滚。
                        </p>
                      </Form>,
                    )
                  }
                >
                  核对运行结果
                </Button>
              )}
            </div>
          </div>
        );
      })}
    </section>
  );
}
