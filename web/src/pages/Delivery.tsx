import { useState, type ReactNode } from "react";
import {
  ArrowRight,
  Check,
  CheckCircle2,
  ChevronRight,
  Clock3,
  FileCheck2,
  GitBranch,
  Layers3,
  LoaderCircle,
  Plus,
  RotateCcw,
  Target,
  Workflow,
} from "lucide-react";
import {
  date,
  money,
  methods,
  roles,
  targets,
  type Data,
  type Resource,
} from "../api";
import { Badge, Button, Confirm, Empty, Form, SectionHeading } from "../ui";
import { useWorkspace } from "../context";
import { GoalForm } from "../forms";
import { SourceRecoveryActions } from "../SourceRecovery";
import { SandboxRecovery } from "../SandboxRecovery";
import {
  AgentExecutionSteps,
  AgentProgressRecovery,
  AgentProgressStatus,
} from "../AgentProgress";
import { MessageContext } from "../MessageContext";
import { MessageWait } from "../MessageWait";
import { Clarifications } from "../Clarifications";
import { RequirementDrafts } from "../RequirementDrafts";
import { PlanGeneration } from "../PlanGeneration";
import { FrozenReleaseIssues } from "../ReleaseIssues";
import { Designs, DesignSummary } from "../Designs";
import { AcceptanceTrace } from "../AcceptanceTrace";
import { Blueprints, BlueprintSummary } from "../Blueprints";
import { SourceWriteScope } from "../SourceWriteScope";
import { TaskPresentation } from "../TaskPresentation";
import { DeliveryOverview } from "../Overview";
import { WorkbenchTabs } from "../WorkbenchTabs";

function SourceDetails({ data, run }: { data: Data; run?: Data }) {
  const w = useWorkspace();
  const binding = data.sourceBinding;
  const conflict = data.sourceConflict;
  if (!binding && !conflict)
    return ["code", "build", "security"].includes(data.kind) ? (
      <>
        <SourceWriteScope task={data} />
        <p className="muted">领取任务时确定源码输入。</p>
      </>
    ) : null;
  const inputs: Data[] = conflict?.inputs || binding?.inputs || [];
  const execution =
    run ||
    w.items
      .filter((x) => x.kind === "run" && x.data.taskId === binding?.taskId)
      .sort((a, b) => b.createdAt.localeCompare(a.createdAt))[0]?.data;
  return (
    <section className="source-details" aria-label="任务源码">
      <SourceWriteScope task={data} />
      {binding && (
        <>
          <h4>固定源码输入</h4>
          <dl>
            <div>
              <dt>仓库</dt>
              <dd>
                {w.items.find((x) => x.id === binding.repositoryId)?.title ||
                  binding.repositoryId}
              </dd>
            </div>
            <div>
              <dt>提交</dt>
              <dd>
                <code>{binding.commit}</code>
              </dd>
            </div>
            {binding.targetBranch && (
              <div>
                <dt>工作分支</dt>
                <dd>{binding.targetBranch}</dd>
              </div>
            )}
            <div>
              <dt>需求基线</dt>
              <dd>v{binding.requirementRevision}</dd>
            </div>
          </dl>
          <p className="muted">
            重试保留此输入。更换源码需要创建新的批准工作项。
          </p>
        </>
      )}
      {conflict && (
        <>
          <h4>源码需要处理</h4>
          <p className="blocked-reason">{conflict.message}</p>
        </>
      )}
      {data.sourceMode === "integrate" && (
        <>
          <h4>受控源码集成</h4>
          {execution?.integrationId ? (
            <>
              <dl>
                <div>
                  <dt>准备记录</dt>
                  <dd>
                    <code>{execution.integrationId}</code>
                  </dd>
                </div>
                <div>
                  <dt>合并工作区</dt>
                  <dd>
                    <code>{execution.integrationWorkspaceCommit}</code>
                  </dd>
                </div>
              </dl>
              {execution.integrationConflicts?.length > 0 ? (
                <ul className="source-inputs" aria-label="集成冲突">
                  {execution.integrationConflicts.map((path: string) => (
                    <li key={path}>
                      <code>{path}</code>{" "}
                      <span>
                        {execution.integrationResolutions?.[path]
                          ? "已记录决定"
                          : "待处理"}
                      </span>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="muted">合并准备未发现内容冲突。</p>
              )}
              <p className="muted">
                提交时核验处理决定与实际文件；业务正确性仍需独立测试。
              </p>
            </>
          ) : (
            <p className="muted">领取后准备选定提交的合并工作区。</p>
          )}
        </>
      )}
      {inputs.length > 0 && (
        <>
          <h4>来源工作项</h4>
          <ul className="source-inputs">
            {inputs.map((input) => (
              <li key={input.taskId + input.runId}>
                <strong>
                  {w.items.find((x) => x.id === input.taskId)?.title ||
                    input.logicalId}
                </strong>
                <dl>
                  <div>
                    <dt>提交</dt>
                    <dd>
                      <code>{input.commit}</code>
                    </dd>
                  </div>
                  <div>
                    <dt>执行</dt>
                    <dd>
                      <code>{input.runId}</code>
                    </dd>
                  </div>
                  <div>
                    <dt>仓库</dt>
                    <dd>
                      {w.items.find((x) => x.id === input.repositoryId)
                        ?.title || input.repositoryId}
                    </dd>
                  </div>
                </dl>
              </li>
            ))}
          </ul>
        </>
      )}
    </section>
  );
}

function TaskDetails({ taskId }: { taskId: string }) {
  const w = useWorkspace();
  const task = w.items.find(
    (item) => item.id === taskId && item.kind === "task",
  );
  if (!task) return <p className="muted">当前项目中无法读取此工作项。</p>;
  return (
    <div className="detail-content">
      <Badge status={task.status} />
      <p>
        {roles[task.data.role]} · {task.data.runtime} · 尝试{" "}
        {task.data.attempts}/{task.data.maxAttempts}
      </p>
      {task.data.reason && !task.data.sourceConflict && (
        <p className="blocked-reason">{task.data.reason}</p>
      )}
      <SourceDetails data={task.data} />
      {task.data.signingMode === "windows-msix" && (
        <section className="source-details" aria-label="Windows MSIX 签名">
          <h4>Windows MSIX 签名</h4>
          <p>
            {task.status === "succeeded"
              ? "签名 MSIX 已生成，独立验证结果见质量中心。"
              : ["blocked", "failed"].includes(task.status)
                ? "签名结果需要核对，重新签名前需批准新的方案。"
                : "由专用 Windows 节点为准确安装包签名。"}
          </p>
          <dl className="source-inputs">
            <div>
              <dt>批准的发布者</dt>
              <dd>
                <code>{task.data.expectedWindowsPublisher}</code>
              </dd>
            </div>
            <div>
              <dt>应用标识</dt>
              <dd>
                <code>{task.data.expectedWindowsPackage}</code>
              </dd>
            </div>
            <div>
              <dt>批准的包版本</dt>
              <dd>
                <code>{task.data.expectedWindowsVersion}</code>
              </dd>
            </div>
            <div>
              <dt>批准的签名证书</dt>
              <dd>
                <code>{task.data.expectedSigner}</code>
              </dd>
            </div>
          </dl>
          {task.status !== "succeeded" && <p>尚未生成最终签名 MSIX。</p>}
          <p className="muted">
            正式交付仍需 Windows 安装与业务验证、发布批准和公开分发验收。
          </p>
        </section>
      )}
      {task.data.signingMode === "ios-ipa" && (
        <section className="source-details" aria-label="iOS App Store 签名">
          <h4>iOS App Store 签名</h4>
          <p>
            {task.status === "succeeded"
              ? "已生成签名 IPA，等待独立验证。"
              : task.status === "blocked"
                ? "签名结果需要核对，重新签名前需批准新的方案。"
                : "由专用 macOS 节点为准确应用签名。"}
          </p>
          <dl className="source-inputs">
            <div>
              <dt>Distribution 团队</dt>
              <dd>
                <code>{task.data.expectedTeamId}</code>
              </dd>
            </div>
            <div>
              <dt>批准的 App Store profile</dt>
              <dd>
                <code>{task.data.expectedProvisioningProfile}</code>
              </dd>
            </div>
          </dl>
          {task.status !== "succeeded" && <p>尚未生成最终签名 IPA。</p>}
          <p className="muted">
            正式交付仍需真机业务验证、发布批准和上架验收。
          </p>
        </section>
      )}
      {task.data.signingMode === "macos-pkg" && (
        <section className="source-details" aria-label="macOS 签名与公证">
          <h4>macOS 签名与公证</h4>
          <p>
            {task.status === "awaiting_signature"
              ? "签名节点已释放，平台将查询原公证结果。"
              : task.status === "blocked"
                ? "签名结果需要核对，重新签名前需批准新的方案。"
                : task.status === "succeeded"
                  ? "签名与公证已完成。"
                  : "由专用 macOS 节点完成签名与公证。"}
          </p>
          <dl className="source-inputs">
            <div>
              <dt>Developer ID 团队</dt>
              <dd>
                <code>{task.data.expectedTeamId}</code>
              </dd>
            </div>
            {task.data.signingOperation?.notarizationId && (
              <div>
                <dt>公证编号</dt>
                <dd>
                  <code>{task.data.signingOperation.notarizationId}</code>
                </dd>
              </div>
            )}
            {task.status === "awaiting_signature" && (
              <>
                <div>
                  <dt>下次查询</dt>
                  <dd>{date(task.data.signingNextCheckAt)}</dd>
                </div>
                <div>
                  <dt>等待截止</dt>
                  <dd>{date(task.data.signingDeadline)}</dd>
                </div>
              </>
            )}
          </dl>
          {task.status !== "succeeded" && <p>尚未生成最终签名安装包。</p>}
          <p className="muted">正式交付仍需独立质量验证和发布验收。</p>
        </section>
      )}
      <AgentExecutionSteps task={task} />
      <MessageWait taskId={task.id} />
      <MessageContext taskId={task.id} />
    </div>
  );
}

function ExecutionPresentation({
  plan,
  goalId,
  children,
}: {
  plan?: Resource;
  goalId: string;
  children: ReactNode;
}) {
  const w = useWorkspace();
  if (!plan) return children;
  return (
    <TaskPresentation
      scope={{
        projectId: w.project.id,
        goalId,
        planId: plan.id,
        requirementRevision: plan.data.requirementRevision,
      }}
      plan={plan}
      resources={w.items}
      mode="execution"
      label="执行工作项展示方式"
      graphLabel="执行依赖图"
      onOpenTask={(taskId) =>
        w.open(
          w.items.find((item) => item.id === taskId)?.title || "工作项详情",
          <TaskDetails taskId={taskId} />,
        )
      }
    >
      {children}
    </TaskPresentation>
  );
}

export function Delivery() {
  const w = useWorkspace();
  const [tab, setTab] = useState("requirements");
  const goals = w.select("goal");
  const g = w.goal;
  const unanswered = w
    .select("clarification")
    .filter(
      (batch) =>
        batch.goalId === g?.id &&
        batch.data.requirementRevision === g?.revision,
    )
    .reduce(
      (count, batch) =>
        count +
        batch.data.questions.filter(
          (question: Data) => question.status !== "answered",
        ).length,
      0,
    );
  const plans = w
    .select("plan")
    .filter((x) => x.goalId === g?.id)
    .sort((a, b) => b.revision - a.revision);
  const activePlan = plans.find((x) => x.status === "active");
  const proposed = plans.find((x) => x.status === "proposed");
  const tasks = w.select("task").filter((x) => x.goalId === g?.id);
  const runs = w.select("run").filter((x) => x.goalId === g?.id);
  const pending =
    goals.filter((x) => x.status === "awaiting_baseline").length +
    w.select("plan").filter((plan) => plan.status === "proposed").length +
    w
      .select("release")
      .filter((release) => release.status === "awaiting_approval").length;
  const currentTasks = tasks.filter(
    (task) =>
      task.data.requirementRevision === g?.revision &&
      (!activePlan || task.data.planId === activePlan.id),
  );
  const blockedTasks = currentTasks.filter((task) =>
    [
      "blocked",
      "failed",
      "waiting_message",
      "suspended",
      "awaiting_signature",
    ].includes(task.status),
  );
  return (
    <>
      <SectionHeading
        eyebrow="DELIVERY SPACE"
        title="从目标，到可验证的交付。"
        description={
          w.project.data.description ||
          "在同一个空间中连接需求、实现、质量与发布。"
        }
        action={
          <Button onClick={() => w.open("新建交付目标", <GoalForm />)}>
            <Plus size={16} />
            新建交付目标
          </Button>
        }
      />
      <DeliveryOverview goals={goals} tasks={currentTasks} pending={pending} />
      {blockedTasks.length > 0 && (
        <div className="attention-strip" role="status">
          <div>
            <strong>{blockedTasks.length} 个当前工作项需要关注</strong>
            <small>
              保留原始失败、协作等待与签名记录；不会自动跳过人工核对。
            </small>
          </div>
          <Button secondary onClick={() => setTab("execution")}>
            查看执行阻塞
          </Button>
        </div>
      )}
      {!g ? (
        <Empty
          icon={<Target size={30} />}
          title="从一个明确的业务目标开始"
          description="说明谁将使用应用、需要完成什么，以及如何验收。平台会保留需求到发布的完整记录。"
          action={
            <Button onClick={() => w.open("新建交付目标", <GoalForm />)}>
              <Plus size={16} />
              创建第一个目标
            </Button>
          }
        />
      ) : (
        <>
          <div className="goal-header">
            <div>
              <span className="mono muted">
                {g.id.slice(-8).toUpperCase()} · 基线 v{g.revision}
              </span>
              <h2>{g.title}</h2>
            </div>
            <Badge status={g.status} />
          </div>
          <div
            className="stage-track"
            role="list"
            aria-label="交付流程阶段"
            tabIndex={0}
          >
            {[
              "需求基线",
              "交付方案",
              "协作开发",
              "独立验证",
              "发布审批",
              "渠道交付",
            ].map((stage, i) => {
              const step =
                g.status === "awaiting_baseline"
                  ? 0
                  : g.status === "planning"
                    ? 1
                    : g.status === "developing"
                      ? 2
                      : g.status === "publishing"
                        ? 5
                        : g.status === "delivered"
                          ? 6
                          : 2;
              return (
                <div
                  className={
                    i < step
                      ? "stage done"
                      : i === step
                        ? "stage current"
                        : "stage"
                  }
                  key={stage}
                  role="listitem"
                >
                  <span>
                    {i < step ? (
                      <Check size={12} />
                    ) : (
                      String(i + 1).padStart(2, "0")
                    )}
                  </span>
                  {stage}
                  {i < 5 && <ChevronRight size={13} />}
                </div>
              );
            })}
          </div>
          <div className="detail-layout">
            <div className="primary-detail">
              <WorkbenchTabs
                label="目标详情"
                value={tab}
                onChange={setTab}
                indicators={proposed ? ["plan"] : []}
                items={[
                  ["requirements", "需求基线"],
                  ["plan", "交付方案"],
                  ["execution", "执行记录"],
                  ["trace", "验收追溯"],
                ]}
              >
                {tab === "trace" && <AcceptanceTrace key={g.id} />}
                {tab === "requirements" && (
                  <div className="detail-content">
                    <div className="content-heading">
                      <h3>业务结果</h3>
                      <button
                        type="button"
                        className="text-button"
                        onClick={() =>
                          w.open("变更需求基线", <GoalForm goal={g} />)
                        }
                      >
                        变更基线
                        <ArrowRight size={13} />
                      </button>
                    </div>
                    <p className="outcome-text">{g.data.outcome}</p>
                    {g.data.requirementBrief && (
                      <section aria-label="需求说明">
                        <div className="content-heading spaced">
                          <h3>需求说明</h3>
                        </div>
                        <p className="requirement-brief">
                          {g.data.requirementBrief}
                        </p>
                      </section>
                    )}
                    <div className="content-heading spaced">
                      <h3>验收标准</h3>
                      <span className="muted">{g.data.criteria.length} 项</span>
                    </div>
                    <div className="criteria-list">
                      {!g.data.criteria.length && (
                        <p className="inline-empty">请先整理至少一项验收标准</p>
                      )}
                      {g.data.criteria.map((c: Data, i: number) => (
                        <div className="criterion" key={c.id}>
                          <span className="criterion-number">
                            {String(i + 1).padStart(2, "0")}
                          </span>
                          <div>
                            <p>{c.text}</p>
                            <span className="mono subtle">{c.id}</span>
                          </div>
                          <span className="method-tag">
                            {methods[c.method] || c.method}
                          </span>
                        </div>
                      ))}
                    </div>
                    <div className="content-heading spaced">
                      <h3>必选客户端与渠道</h3>
                      <span className="muted">逐项验证</span>
                    </div>
                    <div className="target-tags">
                      {g.data.targets.map((t: string) => (
                        <span key={t}>
                          <Layers3 size={13} />
                          {targets[t]}
                        </span>
                      ))}
                    </div>
                    <Clarifications />
                    <RequirementDrafts />
                    {g.status === "awaiting_baseline" && (
                      <div className="decision-panel">
                        <FileCheck2 size={25} />
                        <div>
                          <strong>需求基线等待确认</strong>
                          <p>确认业务范围和验收条件后，才能进入方案与执行。</p>
                        </div>
                        <Button
                          disabled={
                            unanswered > 0 ||
                            !g.data.criteria.length ||
                            w.actor.kind !== "human" ||
                            !w.can("product")
                          }
                          onClick={() =>
                            w.open(
                              "确认需求基线",
                              <Confirm
                                description={
                                  <>
                                    <p>
                                      批准 <strong>{g.title}</strong> 的需求基线
                                      v{g.revision}。
                                    </p>
                                    <p>
                                      包含 {g.data.criteria.length} 项验收标准、
                                      {g.data.targets.length} 个必选目标和{" "}
                                      {money(g.data.budgetCents)} 预算。
                                    </p>
                                  </>
                                }
                                onConfirm={async () => {
                                  await w.run("goals.approve", {
                                    id: g.id,
                                    expectedRequirementRevision: g.revision,
                                  });
                                  w.close();
                                }}
                              />,
                            )
                          }
                        >
                          审阅并确认
                          <ArrowRight size={15} />
                        </Button>
                      </div>
                    )}
                  </div>
                )}
                {tab === "plan" && (
                  <div className="detail-content">
                    <Designs />
                    <Blueprints key={"blueprints-" + g.id} />
                    <PlanGeneration key={g.id} />
                    {!plans.length ? (
                      <Empty
                        icon={<Workflow size={28} />}
                        title="交付路径等待规划"
                        description="需求确认后，可由 Agent 生成方案，或由架构师编制工作项、依赖与验证路径。"
                      />
                    ) : (
                      plans.map((plan) => (
                        <article
                          className="plan-section"
                          key={plan.id}
                          aria-label={"交付方案 v" + plan.revision}
                        >
                          <div className="content-heading">
                            <h4>方案 v{plan.revision}</h4>
                            <Badge status={plan.status} />
                          </div>
                          <p className="outcome-text">{plan.data.summary}</p>
                          <FrozenReleaseIssues
                            entries={plan.data.releaseIssues}
                          />
                          <DesignSummary snapshot={plan.data.design} />
                          <BlueprintSummary
                            snapshot={plan.data.blueprint}
                            initialize={plan.data.initializeBlueprint}
                          />
                          {plan.data.generationId && (
                            <p className="form-note">
                              由模型生成 · 需求 v{plan.data.requirementRevision}{" "}
                              · 生成与费用来源见方案生成记录
                            </p>
                          )}
                          {plan.data.risk && (
                            <p className="risk-note">
                              待关注 · {plan.data.risk}
                            </p>
                          )}
                          <TaskPresentation
                            key={plan.id + "/" + plan.data.requirementRevision}
                            scope={{
                              projectId: w.project.id,
                              goalId: g.id,
                              planId: plan.id,
                              requirementRevision:
                                plan.data.requirementRevision,
                            }}
                            plan={plan}
                            resources={w.items}
                            mode="plan"
                            label={"交付方案 v" + plan.revision + "展示方式"}
                            graphLabel="方案依赖图"
                            onOpenTask={(taskId) =>
                              w.open(
                                w.items.find((item) => item.id === taskId)
                                  ?.title || "工作项详情",
                                <TaskDetails taskId={taskId} />,
                              )
                            }
                          >
                            <div className="plan-tasks">
                              {plan.data.tasks.map((task: Data, i: number) => (
                                <div className="plan-task" key={task.id}>
                                  <span className="task-step">
                                    {String(i + 1).padStart(2, "0")}
                                  </span>
                                  <div>
                                    <strong>{task.title}</strong>
                                    <small>
                                      {roles[task.role]} · {task.runtime}
                                      {task.dependsOn?.length
                                        ? " · 依赖 " + task.dependsOn.join("、")
                                        : ""}
                                    </small>
                                    <SourceWriteScope task={task} />
                                  </div>
                                </div>
                              ))}
                            </div>
                          </TaskPresentation>
                          {plan.status === "proposed" && (
                            <div className="decision-panel compact">
                              <div>
                                <strong>需要架构负责人批准</strong>
                                <p>
                                  批准后创建执行工作项，执行节点按权限和能力领取。
                                </p>
                              </div>
                              <Button
                                disabled={
                                  w.actor.kind !== "human" ||
                                  !w.can("architect")
                                }
                                onClick={() =>
                                  w.open(
                                    "批准交付方案",
                                    <Confirm
                                      description={
                                        <>
                                          <p>
                                            批准方案 v{plan.revision} 并启动其中{" "}
                                            {plan.data.tasks.length} 个工作项。
                                          </p>
                                          <p>
                                            新方案会替代之前的方案，相关发布候选需要重新确认。
                                          </p>
                                          {plan.data.tasks
                                            .filter(
                                              (task: Data) =>
                                                task.kind === "code",
                                            )
                                            .map((task: Data) => (
                                              <section key={task.id}>
                                                <strong>{task.title}</strong>
                                                <SourceWriteScope task={task} />
                                              </section>
                                            ))}
                                          <DesignSummary
                                            snapshot={plan.data.design}
                                          />
                                          <FrozenReleaseIssues
                                            entries={plan.data.releaseIssues}
                                          />
                                          <BlueprintSummary
                                            snapshot={plan.data.blueprint}
                                            initialize={
                                              plan.data.initializeBlueprint
                                            }
                                          />
                                        </>
                                      }
                                      onConfirm={async () => {
                                        await w.run("plans.approve", {
                                          id: plan.id,
                                        });
                                        w.close();
                                        setTab("execution");
                                      }}
                                    />,
                                  )
                                }
                              >
                                批准方案
                                <Check size={15} />
                              </Button>
                            </div>
                          )}
                        </article>
                      ))
                    )}
                  </div>
                )}
                {tab === "execution" && (
                  <div className="detail-content">
                    <div className="content-heading">
                      <h3>工作项</h3>
                      <span className="muted">
                        {activePlan
                          ? "当前方案 v" + activePlan.revision
                          : "暂无活动方案"}
                      </span>
                    </div>
                    <SandboxRecovery goalId={g.id} />
                    <AgentProgressRecovery goalId={g.id} />
                    <ExecutionPresentation
                      key={g.id + "/" + (activePlan?.id || "none")}
                      plan={activePlan}
                      goalId={g.id}
                    >
                      {tasks.length ? (
                        <div className="work-list">
                          {tasks.map((task) => (
                            <div className="work-row" key={task.id}>
                              <div className="work-icon">
                                {task.status === "succeeded" ? (
                                  <CheckCircle2 size={18} />
                                ) : task.status === "running" ? (
                                  <LoaderCircle className="spin" size={18} />
                                ) : (
                                  <Clock3 size={18} />
                                )}
                              </div>
                              <div className="work-info">
                                <button
                                  type="button"
                                  className="work-title"
                                  aria-label={"查看工作项：" + task.title}
                                  onClick={() =>
                                    w.open(
                                      task.title,
                                      <TaskDetails taskId={task.id} />,
                                    )
                                  }
                                >
                                  <strong>{task.title}</strong>
                                </button>
                                <small>
                                  {roles[task.data.role]} · {task.data.runtime}{" "}
                                  · 尝试 {task.data.attempts}/
                                  {task.data.maxAttempts}
                                  {task.data.reason && (
                                    <span className="blocked-reason">
                                      {task.data.reason}
                                    </span>
                                  )}
                                </small>
                                <SourceRecoveryActions task={task} />
                                <AgentProgressStatus task={task} />
                              </div>
                              <Badge status={task.status} />
                              {["failed", "blocked"].includes(task.status) &&
                                !task.data.signingMode && (
                                  <button
                                    type="button"
                                    className="icon-button"
                                    aria-label="重试工作项"
                                    disabled={
                                      w.actor.kind !== "human" ||
                                      !w.can(
                                        ...(task.data.qualityPolicyId
                                          ? ["quality"]
                                          : ["architect", "developer"]),
                                      )
                                    }
                                    onClick={() =>
                                      w.open(
                                        "重试工作项",
                                        <Confirm
                                          label="重试"
                                          description={
                                            <p>
                                              重试「{task.title}
                                              」，保留此前失败记录。
                                            </p>
                                          }
                                          onConfirm={async () => {
                                            await w.run("tasks.retry", {
                                              id: task.id,
                                            });
                                            w.close();
                                          }}
                                        />,
                                      )
                                    }
                                  >
                                    <RotateCcw size={16} />
                                  </button>
                                )}
                            </div>
                          ))}
                        </div>
                      ) : (
                        <Empty
                          title="暂无执行工作项"
                          description="批准交付方案后，平台会创建工作项。没有匹配的执行节点时，工作项保持等待。"
                        />
                      )}
                    </ExecutionPresentation>
                    {runs.length > 0 && (
                      <>
                        <div className="content-heading spaced">
                          <h3>执行历史</h3>
                        </div>
                        {runs.map((run) => (
                          <button
                            type="button"
                            key={run.id}
                            className="resource-row clickable"
                            onClick={() =>
                              w.open(
                                run.title,
                                <div className="detail-content">
                                  <Badge status={run.status} />
                                  <p>{run.data.progress || "尚无进度消息"}</p>
                                  <SourceDetails
                                    data={run.data.task || {}}
                                    run={run.data}
                                  />
                                  <MessageContext taskId={run.data.taskId} />
                                  <MessageWait taskId={run.data.taskId} />
                                  {w.items.find(
                                    (item) => item.id === run.data.taskId,
                                  ) && (
                                    <AgentExecutionSteps
                                      task={
                                        w.items.find(
                                          (item) => item.id === run.data.taskId,
                                        )!
                                      }
                                      runId={run.id}
                                    />
                                  )}
                                  <pre className="code-block">
                                    {run.data.log || "尚无执行日志"}
                                  </pre>
                                </div>,
                              )
                            }
                          >
                            <span>
                              {run.title}
                              <small>
                                {date(run.createdAt)} · {run.data.workerId}
                              </small>
                            </span>
                            <Badge status={run.status} />
                            <ArrowRight size={15} />
                          </button>
                        ))}
                      </>
                    )}
                  </div>
                )}
              </WorkbenchTabs>
            </div>
            <aside className="goal-inspector">
              <h3>交付上下文</h3>
              <dl>
                <div>
                  <dt>需求版本</dt>
                  <dd>
                    v{g.revision}
                    {g.data.approvedRevision === g.revision && (
                      <Check size={14} />
                    )}
                  </dd>
                </div>
                <div>
                  <dt>执行预算</dt>
                  <dd>{money(g.data.budgetCents)}</dd>
                </div>
                <div>
                  <dt>模型费用</dt>
                  <dd>{money(g.data.spentCents)}</dd>
                </div>
                <div>
                  <dt>创建时间</dt>
                  <dd>{date(g.createdAt)}</dd>
                </div>
              </dl>
              <div className="inspector-divider" />
              <h3>关联资产</h3>
              {[
                ["repository", "代码仓库", "code"],
                ["artifact", "构建制品", "quality"],
                ["evidence", "验证证据", "quality"],
                ["release", "发布版本", "releases"],
              ].map(([kind, title, view]) => (
                <button
                  type="button"
                  key={kind}
                  onClick={() =>
                    w.navigate(view as "code" | "quality" | "releases")
                  }
                >
                  <span>
                    {kind === "repository" ? (
                      <GitBranch size={15} />
                    ) : (
                      <Layers3 size={15} />
                    )}{" "}
                    {title}
                  </span>
                  <span>
                    {
                      w
                        .select(kind)
                        .filter((x) => !x.goalId || x.goalId === g.id).length
                    }
                    <ChevronRight size={13} />
                  </span>
                </button>
              ))}
              <div className="inspector-note">
                <Workflow size={17} />
                <p>
                  Agent 可以调整执行路径。需求、质量与发布规则始终由控制面执行。
                </p>
              </div>
            </aside>
          </div>
        </>
      )}
      {goals.length > 1 && (
        <section className="other-goals">
          <SectionHeading title="项目中的交付目标" />
          {goals.map((x) => (
            <button
              type="button"
              key={x.id}
              className={
                "resource-row clickable " + (x.id === g?.id ? "selected" : "")
              }
              onClick={() => w.setGoal(x.id)}
            >
              <Target size={17} />
              <span>
                {x.title}
                <small>
                  需求基线 v{x.revision} · {date(x.createdAt)}
                </small>
              </span>
              <Badge status={x.status} />
              <ArrowRight size={15} />
            </button>
          ))}
        </section>
      )}
    </>
  );
}
