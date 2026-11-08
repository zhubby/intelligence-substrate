import { useEffect, useState } from "react";
import {
  Check,
  ChevronDown,
  Copy,
  ExternalLink,
  KeyRound,
  Plus,
  Radio,
  ShieldCheck,
  Terminal,
  Upload,
  Workflow,
} from "lucide-react";
import {
  api,
  date,
  methods,
  post,
  roles,
  targets,
  type Data,
  type Resource,
} from "../api";
import { Badge, Button, Confirm, Empty, Form, SectionHeading } from "../ui";
import { useWorkspace } from "../context";
import { ChannelForm, ReleaseForm, WorkerForm } from "../forms";
import { RuntimePanel } from "./Runtime";
import { Members } from "../Members";
import { OrganizationTeams } from "../OrganizationTeams";
import { DeliveryCosts } from "../DeliveryCosts";
import { DeliveryMetrics } from "../DeliveryMetrics";
import { FrozenReleaseIssues, ReleaseIssues } from "../ReleaseIssues";

export function Releases() {
  const w = useWorkspace();
  const releases = w
    .select("release")
    .filter((x) => !w.goal || x.goalId === w.goal.id);
  const channelBusy = (releaseId: string, target: string, kinds: string[]) =>
    w
      .select("task")
      .some(
        (x) =>
          kinds.includes(x.data.kind) &&
          x.data.releaseId === releaseId &&
          x.data.target === target &&
          ["pending", "running"].includes(x.status),
      );
  return (
    <>
      <SectionHeading
        eyebrow="BUILD & RELEASE"
        title="多端构建与正式交付"
        description="批准具体版本，跟踪每个渠道的实际结果。发布后独立验证通过，才计为完成交付。"
        action={
          <Button
            disabled={!w.goal || !w.can("release", "developer")}
            onClick={() => w.open("创建发布候选", <ReleaseForm />)}
          >
            <Plus size={16} />
            创建候选版本
          </Button>
        }
      />
      <RuntimePanel />
      {!releases.length ? (
        <Empty
          icon={<Upload size={29} />}
          title="还没有发布候选版本"
          description="准备全部必选目标的应用制品，完成独立验收后，即可创建并审阅候选版本。"
        />
      ) : (
        releases.map((release) => (
          <article className="release-section" key={release.id}>
            <div className="release-heading">
              <div>
                <span className="eyebrow">RELEASE CANDIDATE</span>
                <h3>{release.title}</h3>
                <p className="mono">
                  {release.data.commit.slice(0, 12)} · 需求 v
                  {release.data.requirementRevision}
                </p>
              </div>
              <Badge status={release.status} />
              {release.status === "awaiting_approval" && (
                <Button
                  disabled={!w.can("release")}
                  onClick={() =>
                    w.open(
                      "批准正式发布",
                      <Confirm
                        description={
                          <>
                            <p>
                              批准版本 <strong>{release.title}</strong>
                              ，绑定当前候选制品与需求版本。
                            </p>
                            <p>
                              批准后仍需可信发布服务执行；渠道审核和发布验证分别记录。
                            </p>
                            {release.data.storeMetadata?.length > 0 && (
                              <p>
                                批准同时绑定所列商店资料；拒审后的文字修订需要单独提交资料审批。
                              </p>
                            )}
                          </>
                        }
                        onConfirm={async () => {
                          await w.run("releases.approve", { id: release.id });
                          w.close();
                        }}
                      />,
                    )
                  }
                >
                  <Check size={15} />
                  审阅并批准
                </Button>
              )}
            </div>
            <FrozenReleaseIssues entries={release.data.releaseIssues} />
            <ReleaseIssues release={release} />
            {release.data.storeMetadata?.length > 0 && (
              <section
                className="source-details release-materials"
                aria-label="已绑定商店资料"
              >
                <h4>
                  {release.data.targets.some((t: Data) => t.materialRevisionId)
                    ? "候选原批准资料"
                    : "已绑定商店资料"}
                </h4>
                {release.data.storeMetadata.map((material: Data) => (
                  <div key={material.id}>
                    <a
                      className="text-button"
                      href={"/api/v1/artifacts/" + material.id + "/content"}
                      target="_blank"
                      rel="noopener noreferrer"
                    >
                      查看 App Store 资料清单
                    </a>
                    <p className="mono">{material.digest}</p>
                  </div>
                ))}
              </section>
            )}
            {release.data.targets.some(
              (target: Data) => target.target === "ios-appstore",
            ) &&
              !release.data.storeMetadata?.length && (
                <p className="muted release-materials">
                  App Store 资料由企业预备，本候选未绑定资料清单。
                </p>
              )}
            <div className="release-targets">
              {release.data.targets.map((t: Data) => (
                <div className="release-target" key={t.target}>
                  <div className="target-platform">
                    <span className="platform-icon">
                      {t.target === "web"
                        ? "W"
                        : t.target === "windows"
                          ? "⊞"
                          : t.target === "macos"
                            ? "M"
                            : t.target.startsWith("ios")
                              ? "i"
                              : "A"}
                    </span>
                    <div>
                      <strong>{targets[t.target]}</strong>
                      <small>{t.externalId || "尚无外部发布标识"}</small>
                      {t.reason && <p className="blocked-reason">{t.reason}</p>}
                      {t.lastCheckedAt && (
                        <small>
                          上次查询 {date(t.lastCheckedAt)}
                          {t.upstreamState && ` · 渠道返回 ${t.upstreamState}`}
                        </small>
                      )}
                      {t.nextCheckAt &&
                        [
                          "processing",
                          "awaiting_review",
                          "awaiting_release",
                        ].includes(t.status) && (
                          <small>下次查询 {date(t.nextCheckAt)}</small>
                        )}
                      {t.status === "processing" && (
                        <small>
                          {t.continueRequired
                            ? "构建已处理，等待受控提审"
                            : w
                                  .select("task")
                                  .some(
                                    (x) =>
                                      x.data.kind === "publish_continue" &&
                                      x.data.releaseId === release.id &&
                                      x.data.target === t.target &&
                                      ["pending", "running"].includes(x.status),
                                  )
                              ? "正在继续提审"
                              : "等待构建处理，尚未提审"}
                        </small>
                      )}
                      {t.queryError && (
                        <p className="blocked-reason">{t.queryError}</p>
                      )}
                      {t.queryNote && t.queryNote !== t.reason && (
                        <small>{t.queryNote}</small>
                      )}
                      {t.publicationUrl && (
                        <a
                          className="text-button"
                          href={t.publicationUrl}
                          target="_blank"
                          rel="noopener noreferrer"
                        >
                          打开分发地址 <ExternalLink size={13} />
                        </a>
                      )}
                    </div>
                  </div>
                  <Badge status={t.status} />
                  {release.status !== "stale" &&
                    release.status !== "awaiting_approval" && (
                      <div className="button-group">
                        {[
                          "processing",
                          "awaiting_review",
                          "awaiting_release",
                          "published",
                        ].includes(t.status) &&
                          t.externalId && (
                            <Button
                              secondary
                              disabled={
                                !w.can("release") ||
                                w
                                  .select("task")
                                  .some(
                                    (x) =>
                                      [
                                        "publish_status",
                                        "publish_continue",
                                      ].includes(x.data.kind) &&
                                      x.data.releaseId === release.id &&
                                      x.data.target === t.target &&
                                      ["pending", "running"].includes(x.status),
                                  )
                              }
                              onClick={() =>
                                w.open(
                                  "查询渠道实际状态",
                                  <Form
                                    fields={[
                                      {
                                        name: "workerId",
                                        label: "查询节点",
                                        type: "select",
                                        required: true,
                                        defaultValue: t.publisherWorkerId,
                                        options: Object.fromEntries(
                                          w
                                            .select("worker")
                                            .filter(
                                              (x) =>
                                                x.data.roles.includes(
                                                  "release",
                                                ) &&
                                                x.data.capabilities.includes(
                                                  "publish",
                                                ) &&
                                                x.status !== "disabled",
                                            )
                                            .map((x) => [x.id, x.title]),
                                        ),
                                      },
                                    ]}
                                    submit="提交状态查询"
                                    onSubmit={async (v) => {
                                      await w.run("releases.refresh", {
                                        id: release.id,
                                        target: t.target,
                                        workerId: v.workerId,
                                        key: crypto.randomUUID(),
                                      });
                                      w.close();
                                    }}
                                  >
                                    <p className="form-note">
                                      节点只查询已有外部版本。查询结果不会替代发布后的独立验收；等待处理和审核期间释放执行资源；需要提审时由独立任务检查原审批后执行。
                                    </p>
                                  </Form>,
                                )
                              }
                            >
                              {channelBusy(release.id, t.target, [
                                "publish_continue",
                              ])
                                ? "提审处理中"
                                : channelBusy(release.id, t.target, [
                                      "publish_status",
                                    ])
                                  ? "查询已排队"
                                  : "查询渠道"}
                            </Button>
                          )}
                        {t.status === "ready" && (
                          <Button
                            secondary
                            disabled={!w.can("release")}
                            onClick={() =>
                              w.open(
                                "交给可信发布节点",
                                <Form
                                  fields={[
                                    {
                                      name: "workerId",
                                      label: "发布节点",
                                      type: "select",
                                      required: true,
                                      options: Object.fromEntries(
                                        w
                                          .select("worker")
                                          .filter(
                                            (x) =>
                                              x.data.roles.includes(
                                                "release",
                                              ) &&
                                              x.data.capabilities.includes(
                                                "publish",
                                              ) &&
                                              x.status !== "disabled",
                                          )
                                          .map((x) => [x.id, x.title]),
                                      ),
                                    },
                                  ]}
                                  submit="创建发布操作"
                                  onSubmit={async (v) => {
                                    await w.run("releases.dispatch", {
                                      id: release.id,
                                      target: t.target,
                                      workerId: v.workerId,
                                      key: crypto.randomUUID(),
                                    });
                                    w.close();
                                  }}
                                >
                                  <p className="form-note">
                                    只执行当前已批准的候选制品。节点必须配置此渠道的可信发布程序。
                                  </p>
                                </Form>,
                              )
                            }
                          >
                            执行发布
                          </Button>
                        )}
                        <button
                          type="button"
                          disabled={!w.can("release")}
                          className="text-button"
                          onClick={() =>
                            w.open(
                              "记录 " + targets[t.target] + " 的实际状态",
                              <ChannelForm release={release} target={t} />,
                            )
                          }
                        >
                          记录结果
                          <ChevronDown size={13} />
                        </button>
                        {t.status === "published" && (
                          <button
                            type="button"
                            disabled={!w.can("quality")}
                            className="text-button"
                            onClick={() =>
                              w.open(
                                "独立验证渠道发布",
                                <Form
                                  fields={[
                                    {
                                      name: "evidenceId",
                                      label: "发布后独立验证证据",
                                      type: "select",
                                      required: true,
                                      options: Object.fromEntries(
                                        w
                                          .select("evidence")
                                          .filter(
                                            (x) =>
                                              x.data.purpose ===
                                                "publication" &&
                                              x.data.artifactId ===
                                                t.artifactId &&
                                              (!t.materialRevisionId ||
                                                (x.data.releaseId ===
                                                  release.id &&
                                                  x.data.publicationBinding ===
                                                    t.publicationBinding)),
                                          )
                                          .map((x) => [
                                            x.id,
                                            x.title + " · " + date(x.createdAt),
                                          ]),
                                      ),
                                    },
                                  ]}
                                  submit="核对证据并验证"
                                  onSubmit={async (v) => {
                                    await w.run("releases.verify", {
                                      id: release.id,
                                      target: t.target,
                                      evidenceId: v.evidenceId,
                                    });
                                    w.close();
                                  }}
                                />,
                              )
                            }
                          >
                            <ShieldCheck size={14} />
                            验证发布
                          </button>
                        )}
                      </div>
                    )}
                </div>
              ))}
            </div>
          </article>
        ))
      )}
      <div className="footnote">
        <ShieldCheck size={16} />
        <p>
          商店拒审、外部结果不明和部分渠道失败会保留为独立状态。移动端异常通过暂停分发、兼容性策略与修复版本处理。
        </p>
      </div>
    </>
  );
}

function Token({ token }: { token: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="detail-content">
      <p>凭据仅在本次显示。请交给对应执行节点，保存在受控凭据环境。</p>
      <pre className="secret-token">{token}</pre>
      <Button
        onClick={async () => {
          await navigator.clipboard.writeText(token);
          setCopied(true);
        }}
      >
        <Copy size={15} />
        {copied ? "已复制" : "复制凭据"}
      </Button>
    </div>
  );
}
export function Agents() {
  const w = useWorkspace();
  const workers = w.select("worker");
  return (
    <>
      <SectionHeading
        eyebrow="AGENTS & EXECUTION"
        title="Agent 与执行环境"
        description="按任务激活角色，以受控节点执行工作。未连接的环境保持等待。"
        action={
          <Button
            disabled={!w.can("owner")}
            onClick={() => w.open("注册执行节点", <WorkerForm />)}
          >
            <Plus size={16} />
            注册节点
          </Button>
        }
      />
      <div className="role-strip">
        {[
          "analysis",
          "design",
          "architect",
          "developer",
          "quality",
          "security",
          "release",
        ].map((r, i) => (
          <div key={r}>
            <span className="role-avatar">
              {String(i + 1).padStart(2, "0")}
            </span>
            <strong>{roles[r]}</strong>
            <small>
              {
                w
                  .select("task")
                  .filter((t) => t.data.role === r && t.status === "running")
                  .length
              }{" "}
              个执行中
            </small>
          </div>
        ))}
      </div>
      <SectionHeading
        title="执行节点"
        description="节点身份、系统与能力由项目负责人授权。"
      />
      {workers.length ? (
        workers.map((worker) => (
          <div className="worker-row" key={worker.id}>
            <div className="worker-icon">
              <Terminal size={20} />
            </div>
            <div>
              <strong>{worker.title}</strong>
              <small>
                {worker.data.runtime} ·{" "}
                {worker.data.roles.map((r: string) => roles[r] || r).join("、")}{" "}
                · {worker.data.capabilities.join(" / ")}
              </small>
              <small>
                {worker.data.lastSeen
                  ? "最近连接 " + date(worker.data.lastSeen)
                  : "等待工作进程连接"}
                {worker.data.trustedQuality ? " · 独立质量授权" : ""}
              </small>
            </div>
            <Badge status={worker.status} />
            <Button
              secondary
              disabled={!w.can("owner") || worker.status === "disabled"}
              onClick={() =>
                w.open(
                  "签发节点凭据",
                  <Form
                    fields={[]}
                    submit="签发 30 天有效凭据"
                    onSubmit={async () => {
                      const result = await post<{ token: string }>("tokens", {
                        projectId: w.project.id,
                        workerId: worker.id,
                      });
                      w.open(
                        "保存节点访问凭据",
                        <Token token={result.token} />,
                      );
                      await w.refresh();
                    }}
                  >
                    <p className="form-note">
                      凭据限定到当前项目与此节点，角色由平台记录决定。
                    </p>
                  </Form>,
                )
              }
            >
              <KeyRound size={14} />
              访问凭据
            </Button>
            {worker.status !== "disabled" && w.can("owner") && (
              <Button
                secondary
                onClick={() =>
                  w.open(
                    "停用执行节点",
                    <Confirm
                      description={
                        <p>
                          停止「{worker.title}
                          」领取和继续执行工作。正在运行的发布仍需核对外部结果。
                        </p>
                      }
                      onConfirm={async () => {
                        await w.run("workers.disable", { id: worker.id });
                        w.close();
                      }}
                      label="停用节点"
                    />,
                  )
                }
              >
                停用
              </Button>
            )}
          </div>
        ))
      ) : (
        <Empty
          icon={<Terminal size={28} />}
          title="没有已注册执行节点"
          description="连接 Linux Agent、原生构建或质量验证节点后，节点可以领取匹配的工作项。"
        />
      )}
      <SectionHeading
        title="运行与发布接入"
        description="已配置不代表已验证；实际执行和渠道结果在交付记录中查看。"
      />
      <div className="integration-list">
        {w.integrations.map((x) => (
          <div className="integration-row" key={x.id}>
            <span className="integration-icon">
              <Radio size={17} />
            </span>
            <div>
              <strong>
                {targets[x.id.replace("channel-", "")] || x.title}
              </strong>
              <small>{x.reason}</small>
            </div>
            <Badge status={x.status} />
          </div>
        ))}
      </div>
    </>
  );
}

export function Governance() {
  const w = useWorkspace();
  const [tab, setTab] = useState("audit");
  const knowledge = w.select("knowledge");
  return (
    <>
      <SectionHeading
        eyebrow="GOVERNANCE & OPERATIONS"
        title="企业治理与运行"
        description="项目身份、决策和执行记录共同构成可追溯的交付历史。"
      />
      <div
        className="tabs governance-tabs"
        role="tablist"
        aria-label="治理视图"
      >
        {[
          ["audit", "审计记录"],
          ["members", "项目成员"],
          ["teams", "组织团队"],
          ["knowledge", "项目知识"],
          ["policies", "控制策略"],
          ["messages", "协作消息"],
          ["costs", "交付成本"],
          ["metrics", "交付效能"],
          ...(w.can("owner") ? [["tokens", "访问凭据"]] : []),
        ].map(([k, v]) => (
          <button
            type="button"
            key={k}
            role="tab"
            aria-selected={tab === k}
            className={tab === k ? "active" : ""}
            onClick={() => setTab(k)}
          >
            {v}
          </button>
        ))}
      </div>
      {tab === "tokens" && <Credentials />}
      {tab === "messages" && <Messages />}
      {tab === "costs" && <DeliveryCosts />}
      {tab === "metrics" && <DeliveryMetrics />}
      {tab === "audit" && (
        <div className="audit-timeline">
          {[...w.audits].reverse().map((event) => (
            <div className="audit-event" key={event.id}>
              <span className="audit-mark" />
              <div>
                <strong>{actionName(event.action)}</strong>
                <span className="mono">{event.resourceId?.slice(-12)}</span>
                <small>
                  {date(event.at)} · {event.actorId} ·{" "}
                  {event.detail.status || event.detail.code || ""}
                </small>
              </div>
              <span className="audit-hash mono" title={event.hash}>
                {event.hash.slice(0, 12)}
              </span>
            </div>
          ))}
          {!w.audits.length && (
            <Empty
              title="暂无审计记录"
              description="需求、方案、执行与发布操作会在这里留下记录。"
            />
          )}
        </div>
      )}
      {tab === "members" && <Members />}
      {tab === "teams" && <OrganizationTeams />}
      {tab === "knowledge" && (
        <>
          <SectionHeading
            title="项目知识与来源"
            action={
              <Button
                disabled={!w.can("product", "architect", "developer")}
                onClick={() =>
                  w.open(
                    "添加项目知识",
                    <Form
                      fields={[
                        { name: "title", label: "知识标题", required: true },
                        { name: "source", label: "来源" },
                        {
                          name: "content",
                          label: "内容",
                          type: "textarea",
                          required: true,
                        },
                      ]}
                      onSubmit={async (v) => {
                        await w.run("knowledge.create", {
                          ...v,
                          projectId: w.project.id,
                        });
                        w.close();
                      }}
                    />,
                  )
                }
              >
                <Plus size={15} />
                添加知识
              </Button>
            }
          />
          {knowledge.map((k) => (
            <button
              type="button"
              className="resource-row clickable"
              key={k.id}
              onClick={() =>
                w.open(
                  k.title,
                  <div className="detail-content">
                    <p className="muted">
                      来源：{k.data.source || "项目内记录"}
                    </p>
                    <p className="preserve-lines">{k.data.content}</p>
                    {k.status !== "superseded" &&
                      w.can("product", "architect", "developer") && (
                        <Button
                          secondary
                          onClick={() =>
                            w.open(
                              "修订项目知识",
                              <Form
                                fields={[
                                  {
                                    name: "content",
                                    label: "知识内容",
                                    type: "textarea",
                                    required: true,
                                    defaultValue: k.data.content,
                                  },
                                  {
                                    name: "source",
                                    label: "来源",
                                    defaultValue: k.data.source,
                                  },
                                  {
                                    name: "reason",
                                    label: "修订原因",
                                    required: true,
                                  },
                                ]}
                                submit="保存新版本"
                                onSubmit={async (v) => {
                                  await w.run("knowledge.revise", {
                                    id: k.id,
                                    ...v,
                                  });
                                  w.close();
                                }}
                              />,
                            )
                          }
                        >
                          创建新版本
                        </Button>
                      )}
                  </div>,
                )
              }
            >
              <Workflow size={18} />
              <span>
                {k.title}
                <small>
                  {k.data.source || "项目内记录"} · v{k.revision}
                </small>
              </span>
              <ExternalLink size={14} />
            </button>
          ))}
          {!knowledge.length && (
            <Empty
              title="暂无项目知识"
              description="记录业务背景、领域概念和设计决策，保留版本与来源。"
            />
          )}
        </>
      )}
      {tab === "policies" && (
        <div className="policy-list">
          {w.can("owner") && (
            <Button
              secondary
              onClick={() =>
                w.open(
                  "调整项目模型访问策略",
                  <Form
                    fields={[
                      {
                        name: "allowExternalModels",
                        label: "允许访问外部模型",
                        type: "checkbox",
                        defaultValue: w.project.data.allowExternalModels,
                      },
                    ]}
                    submit="保存策略"
                    onSubmit={async (v) => {
                      await w.run("projects.policy", {
                        id: w.project.id,
                        ...v,
                      });
                      w.close();
                    }}
                  />,
                )
              }
            >
              调整外部模型访问
            </Button>
          )}
          {[
            ["需求与方案", "需求基线、重大方案及正式发布由有权限的人批准。"],
            [
              "独立质量",
              "开发主体不能为自己的制品提供独立验收；自动化证据来自授权质量节点。",
            ],
            [
              "版本绑定",
              "审批与验证绑定需求版本、代码提交和制品摘要，相关变更触发重新确认。",
            ],
            [
              "执行控制",
              "执行节点按项目、系统与能力领取任务，任务具有租约、重试和预算限制。",
            ],
            [
              "外部模型",
              w.project.data.allowExternalModels
                ? "此项目允许按平台配置访问外部模型。"
                : "此项目未授权外部模型访问。",
            ],
            [
              "正式交付",
              "全部必选目标发布并经过独立验证后，整体版本才标记为交付完成。",
            ],
          ].map(([title, text]) => (
            <div key={title}>
              <ShieldCheck size={19} />
              <div>
                <h3>{title}</h3>
                <p>{text}</p>
              </div>
            </div>
          ))}
        </div>
      )}
    </>
  );
}
function Credentials() {
  const w = useWorkspace();
  const [tokens, setTokens] = useState<Data[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    let live = true;
    setLoading(true);
    api<Data[]>("tokens?projectId=" + encodeURIComponent(w.project.id))
      .then((v) => {
        if (live) {
          setTokens(v);
          setError("");
        }
      })
      .catch((e) => {
        if (live) setError(e.message);
      })
      .finally(() => {
        if (live) setLoading(false);
      });
    return () => {
      live = false;
    };
  }, [w.project.id, w.audits.length]);
  return (
    <>
      <SectionHeading
        title="项目访问凭据"
        description="凭据限制到项目与角色，明文仅在签发时显示。撤销后新请求立即失效。"
      />
      {error && (
        <p role="alert" className="form-error">
          {error}
        </p>
      )}
      {loading ? (
        <div className="inline-empty">正在读取凭据…</div>
      ) : (
        tokens.map((t) => (
          <div className="resource-row" key={t.id}>
            <KeyRound size={18} />
            <span>
              {t.actor.name || t.actor.id}
              <small>
                {t.actor.kind} · {t.id.slice(0, 12)} · 到期 {date(t.expiresAt)}
              </small>
            </span>
            <Button
              secondary
              onClick={() =>
                w.open(
                  "撤销访问凭据",
                  <Confirm
                    label="撤销凭据"
                    description={
                      <p>撤销此凭据，使用它的执行进程或客户端需要重新授权。</p>
                    }
                    onConfirm={async () => {
                      await post("tokens/revoke", {
                        id: t.id,
                        projectId: w.project.id,
                      });
                      await w.refresh();
                      w.close();
                    }}
                  />,
                )
              }
            >
              撤销
            </Button>
          </div>
        ))
      )}
      {!loading && !tokens.length && (
        <div className="inline-empty">
          尚无项目访问凭据，可在执行节点中签发。
        </div>
      )}
    </>
  );
}

function Messages() {
  const w = useWorkspace();
  const messages = w.select("message");
  const recipients = Object.fromEntries([
    ...w
      .select("member")
      .map((x) => [x.data.identityId, x.title || x.data.identityId]),
    ...w
      .select("worker")
      .filter((x) => x.status !== "disabled")
      .map((x) => ["worker:" + x.id, x.title]),
  ]);
  return (
    <>
      <SectionHeading
        title="持久协作消息"
        description="消息按收件人领取并确认，重复请求按幂等键处理。请求与完成声明仍受平台权限与质量门槛约束。"
        action={
          <div className="button-group">
            <Button
              secondary
              onClick={() =>
                w.open(
                  "领取待处理消息",
                  <Form
                    fields={[]}
                    submit="领取下一条"
                    onSubmit={async () => {
                      const m = await w.run("messages.claim", {
                        projectId: w.project.id,
                      });
                      if (m.kind !== "message") {
                        w.close();
                        w.notify("没有发给当前身份的待处理消息");
                        return;
                      }
                      w.open(
                        "处理协作消息",
                        <Confirm
                          label="确认已处理"
                          description={
                            <>
                              <p>
                                来自 {m.data.sender} · {m.data.type}
                              </p>
                              <pre className="run-log">
                                {JSON.stringify(m.data.payload, null, 2)}
                              </pre>
                            </>
                          }
                          onConfirm={async () => {
                            await w.run("messages.ack", {
                              id: m.id,
                              leaseToken: m.data.leaseToken,
                            });
                            w.close();
                          }}
                        />,
                      );
                    }}
                  />,
                )
              }
            >
              领取我的消息
            </Button>
            <Button
              onClick={() =>
                w.open(
                  "发送协作消息",
                  <Form
                    fields={[
                      {
                        name: "recipient",
                        label: "收件人",
                        type: "select",
                        options: recipients,
                        required: true,
                      },
                      {
                        name: "type",
                        label: "消息类型",
                        type: "select",
                        defaultValue: "clarification",
                        options: {
                          request: "任务请求",
                          progress: "进度",
                          result: "产物提交",
                          blocked: "阻塞",
                          clarification: "需求澄清",
                          review: "审查",
                          cancel: "取消请求",
                        },
                      },
                      {
                        name: "text",
                        label: "消息内容",
                        type: "textarea",
                        required: true,
                      },
                      { name: "causationId", label: "关联前序消息 ID（可选）" },
                    ]}
                    submit="发送消息"
                    onSubmit={async (v) => {
                      await w.run("messages.send", {
                        projectId: w.project.id,
                        goalId: w.goal?.id,
                        recipient: v.recipient,
                        type: v.type,
                        causationId: v.causationId,
                        payload: { text: v.text },
                        key: crypto.randomUUID(),
                      });
                      w.close();
                    }}
                  />,
                )
              }
            >
              发送消息
            </Button>
          </div>
        }
      />
      {messages.map((m) => (
        <button
          type="button"
          className="resource-row clickable"
          key={m.id}
          onClick={() =>
            w.open(
              "协作消息",
              <div className="detail-content">
                <p>
                  {m.data.sender} → {m.data.recipient}
                </p>
                <p className="mono">{m.id}</p>
                <pre className="run-log">
                  {JSON.stringify(m.data.payload, null, 2)}
                </pre>
                {m.data.causationId && <p>前序 {m.data.causationId}</p>}
              </div>,
            )
          }
        >
          <Workflow size={17} />
          <span>
            {
              (
                {
                  request: "任务请求",
                  progress: "进度更新",
                  result: "产物提交",
                  blocked: "阻塞",
                  clarification: "澄清",
                  review: "审查",
                  cancel: "取消请求",
                } as Record<string, string>
              )[m.data.type]
            }
            <small>
              {m.data.sender} → {m.data.recipient} · 尝试 {m.data.attempts}/3
            </small>
          </span>
          <Badge status={m.status} />
        </button>
      ))}
      {!messages.length && <div className="inline-empty">暂无协作消息</div>}
    </>
  );
}

function actionName(action: string) {
  const names: Record<string, string> = {
    "projects.create": "创建项目",
    "goals.create": "创建交付目标",
    "goals.approve": "批准需求基线",
    "goals.revise": "变更需求基线",
    "plans.propose": "提交交付方案",
    "plans.approve": "批准交付方案",
    "repositories.create": "创建代码仓库",
    "repositories.commit": "提交代码变更",
    "reviews.create": "发起代码评审",
    "reviews.approve": "批准代码评审",
    "reviews.merge": "合并代码",
    "workers.register": "注册执行节点",
    "tasks.claim": "领取工作项",
    "tasks.complete": "完成执行",
    "tasks.progress": "更新执行进度",
    "tasks.retry": "重试工作项",
    "artifacts.register": "登记实际制品",
    "evidence.record": "登记独立验收",
    "releases.create": "创建发布候选",
    "releases.approve": "批准正式发布",
    "releases.transition": "记录渠道状态",
    "releases.verify": "独立验证发布",
    "members.grant": "更新项目成员",
    "members.revoke": "移除直接项目授权",
    "teams.create": "创建组织团队",
    "teams.update": "修订组织团队",
    "teams.disable": "停用组织团队",
    "teams.enable": "启用组织团队",
    "teams.grant": "更新团队项目授权",
    "teams.revoke": "撤销团队项目授权",
    "teams.access.changed": "团队权限来源变更",
    "knowledge.create": "记录项目知识",
    "knowledge.revise": "修订项目知识",
    "workers.disable": "停用执行节点",
    "tokens.issue": "签发访问凭据",
    "tokens.revoke": "撤销访问凭据",
    "plans.reorder": "调整工作依赖",
    "tasks.split": "拆分工作项",
    "releases.dispatch": "创建正式发布操作",
    "releases.reconcile": "核对外部发布结果",
    "releases.refresh": "查询渠道状态",
    "release_issues.triage": "确认拒审修复要求",
    "release_issues.opened": "记录渠道拒审问题",
    "release_issues.resolved": "独立验收关闭拒审问题",
    "channels.query.scheduled": "安排渠道状态查询",
    "charges.reconcile": "核对模型费用",
    "projects.policy": "调整模型访问策略",
    "pipelines.create": "创建验证策略",
    "pipelines.run": "发起独立验证",
    "model.charge": "记录模型费用",
    "messages.send": "发送协作消息",
    "git.push": "推送代码",
  };
  return action.startsWith("denied:")
    ? "拒绝未授权或无效操作"
    : names[action] || action;
}
