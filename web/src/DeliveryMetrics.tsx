import { useEffect, useState } from "react";
import { RefreshCw } from "lucide-react";
import { api, date, targets, type Resource } from "./api";
import { useWorkspace } from "./context";
import { Badge, Button, Form, SectionHeading, type Field } from "./ui";
import { ReleaseFeedback } from "./ReleaseFeedback";

interface Observation {
  id: string;
  goalId: string;
  requirementRevision: number;
  type: string;
  status: string;
  reference: string;
  note: string;
  recordedBy: string;
  recordedAt: string;
  result?: string;
  artifactId?: string;
  releaseId?: string;
  proofArtifactId?: string;
  proofDigest?: string;
  commit?: string;
  verificationEnvironment?: string;
  startedAt?: string;
  endedAt?: string;
  durationSeconds?: number;
  replacesMeasurementId?: string;
  voidReason?: string;
  voidedBy?: string;
  voidedAt?: string;
  usable: boolean;
  issues: string[];
}
interface MetricVersion {
  goalId: string;
  title: string;
  requirementRevision: number;
  current: boolean;
  firstPreviewId?: string;
  firstAcceptanceId?: string;
}
interface MetricReport {
  requirements: number;
  previewSamples: number;
  meanPreviewSeconds: number | null;
  acceptanceSamples: number;
  firstPassCount: number;
  firstPassRate: number | null;
  effortSamples: number;
  humanSeconds: number | null;
  reportedProductionDefects: number;
  publishExecutions: number;
  failedPublishExecutions: number;
  publishExecutionFailureRate: number | null;
  observedChannelWaitSeconds: number;
  channelHistoryComplete: boolean;
  versions: MetricVersion[];
  observations: Observation[];
  gaps: string[];
}
const labels: Record<string, string> = {
  preview: "可运行预览确认",
  business_acceptance: "业务验收",
  human_effort: "本人投入",
  production_defect: "上线缺陷",
};
const seconds = (value: number | null) =>
  value === null ? "待采集" : value.toFixed(1) + " 秒";
const rate = (value: number | null) =>
  value === null ? "待采集" : (value * 100).toFixed(1) + "%";
const options = (items: Resource[]) =>
  Object.fromEntries(
    items.map((r) => [r.id, r.title + " · " + r.id.slice(-8)]),
  );

export function DeliveryMetrics() {
  const w = useWorkspace();
  const goalId = w.goal?.id || "";
  const [revision, setRevision] = useState({ goalId: "", value: "" });
  const selectedRevision = revision.goalId === goalId ? revision.value : "";
  const scope = w.project.id + "/" + goalId + "/" + selectedRevision;
  const [request, setRequest] = useState<{
    scope: string;
    loading: boolean;
    error?: string;
    report?: MetricReport;
  }>();
  const [refresh, setRefresh] = useState(0);
  const current = request?.scope === scope ? request : undefined;
  const report = current?.report;
  const historical =
    !!w.goal &&
    !!selectedRevision &&
    Number(selectedRevision) !== w.goal.revision;
  const canObserve =
    w.actor.kind === "human" && w.can("product", "quality") && !!w.goal;
  const canWrite = canObserve && !historical;
  const observationRevision = Number(selectedRevision || w.goal?.revision || 0);
  const revisions = w
    .select("requirement")
    .filter((r) => r.goalId === goalId)
    .map((r) => r.revision)
    .sort((a, b) => b - a);
  useEffect(() => {
    let active = true;
    setRequest({ scope, loading: true });
    const query = new URLSearchParams();
    if (goalId) query.set("goalId", goalId);
    if (goalId && selectedRevision) query.set("revision", selectedRevision);
    api<MetricReport>(
      "projects/" +
        encodeURIComponent(w.project.id) +
        "/delivery-metrics" +
        (query.size ? "?" + query : ""),
    )
      .then((data) => {
        if (active) setRequest({ scope, loading: false, report: data });
      })
      .catch((e) => {
        if (active)
          setRequest({ scope, loading: false, error: (e as Error).message });
      });
    return () => {
      active = false;
    };
  }, [scope, w.project.id, goalId, selectedRevision, w.items, refresh]);

  function record(kind: string) {
    const goal = w.goal;
    if (!goal || !(kind === "production_defect" ? canObserve : canWrite))
      return;
    const key = crypto.randomUUID();
    const fields: Field[] = [];
    const reports = w
      .select("artifact")
      .filter(
        (r) =>
          r.goalId === goal.id &&
          r.data.type === "report" &&
          (kind !== "business_acceptance" ||
            r.data.purpose !== "release-feedback") &&
          r.data.requirementRevision === observationRevision,
      );
    if (kind === "preview") {
      const evidence = w
        .select("evidence")
        .filter(
          (r) =>
            r.goalId === goal.id &&
            r.data.requirementRevision === goal.revision &&
            r.status === "passed" &&
            r.data.purpose !== "publication" &&
            r.data.verificationEnvironment &&
            r.data.verificationEnvironment !== "ci",
        );
      fields.push({
        name: "evidenceId",
        label: "独立运行证据",
        type: "select",
        required: true,
        options: Object.fromEntries(
          evidence.map((r) => [
            r.id,
            (targets[r.data.target] || r.data.target) +
              " · " +
              r.data.verificationEnvironment +
              " · " +
              r.id.slice(-8),
          ]),
        ),
        hint: "选择已核验实际运行环境的准确制品证据。记录表示人的预览确认。",
      });
    } else if (kind === "human_effort") {
      fields.push(
        {
          name: "startedAt",
          label: "开始时间",
          type: "datetime-local",
          step: "1",
          required: true,
        },
        {
          name: "endedAt",
          label: "结束时间",
          type: "datetime-local",
          step: "1",
          required: true,
          hint: "登记本人实际投入，区间在需求版本创建后且已经结束；最长 24 小时，不得与本人已有区间重叠。",
        },
      );
    } else {
      fields.push({
        name: "releaseId",
        label: "候选版本",
        type: "select",
        required: true,
        options: options(
          w
            .select("release")
            .filter(
              (r) =>
                r.goalId === goal.id &&
                r.data.requirementRevision === observationRevision,
            ),
        ),
        hint:
          kind === "production_defect"
            ? "只能关联已有真实发布观测的版本；测试缺陷与运行告警不自动计入。"
            : "明确记录这次业务验收的通过或失败，不改变独立质量验证。",
      });
      if (kind === "business_acceptance")
        fields.push({
          name: "result",
          label: "验收结果",
          type: "select",
          required: true,
          options: { failed: "未通过", passed: "通过" },
        });
      fields.push({
        name: "proofArtifactId",
        label: "报告附件",
        type: "select",
        required: true,
        options: options(reports),
        hint: "附件需绑定准确版本的需求、源码提交和目标。",
      });
    }
    fields.push(
      {
        name: "reference",
        label: "观测来源标识",
        required: true,
        hint: "使用稳定的验收会议、工单或投入区间编号，项目内不能重复计入。",
      },
      { name: "note", label: "观测依据", type: "textarea", required: true },
    );
    w.open(
      labels[kind],
      <Form
        fields={fields}
        submit="保存观测"
        onSubmit={async (v) => {
          const input: Record<string, unknown> = {
            goalId: goal.id,
            requirementRevision: observationRevision,
            type: kind,
            key,
            reference: v.reference,
            note: v.note,
          };
          if (kind === "preview") {
            const ev = w.select("evidence").find((r) => r.id === v.evidenceId);
            input.evidenceId = v.evidenceId;
            input.artifactId = ev?.data.artifactId;
            input.proofArtifactId = ev?.data.proofArtifactId;
          } else if (kind === "human_effort") {
            input.startedAt = new Date(v.startedAt).toISOString();
            input.endedAt = new Date(v.endedAt).toISOString();
          } else {
            input.releaseId = v.releaseId;
            input.proofArtifactId = v.proofArtifactId;
            if (kind === "business_acceptance") input.result = v.result;
          }
          await w.run("measurements.record", input);
          w.close();
        }}
      >
        <p className="form-note">
          登记为人工观测，保留来源与需求版本。未登记的数据仍为未知。首次确认预览与业务验收使用平台记录时间。
        </p>
      </Form>,
    );
  }

  function voidRecord(r: Observation) {
    const key = crypto.randomUUID();
    w.open(
      "作废观测",
      <Form
        fields={[
          {
            name: "reason",
            label: "作废原因",
            type: "textarea",
            required: true,
          },
        ]}
        submit="确认作废"
        onSubmit={async (v) => {
          await w.run("measurements.void", { id: r.id, key, reason: v.reason });
          w.close();
        }}
      >
        <p className="form-note">
          保留原始观测和作废原因，统计会按有效记录重新计算；更正时使用原来源标识登记新记录。
        </p>
      </Form>,
    );
  }

  function row(r: Observation) {
    const first = report?.versions.some(
      (v) => v.firstPreviewId === r.id || v.firstAcceptanceId === r.id,
    );
    return (
      <article
        key={r.id}
        className="cost-entry"
        aria-label={"观测记录 " + r.id}
      >
        <div className="cost-entry-heading">
          <div>
            <strong>
              {labels[r.type] || r.type}
              {r.result
                ? " · " + (r.result === "passed" ? "通过" : "未通过")
                : ""}
            </strong>
            <small>
              需求 v{r.requirementRevision} · {date(r.recordedAt)} · 人工登记{" "}
              {r.recordedBy}
            </small>
          </div>
          <Badge status={r.status} />
        </div>
        <p className="mono cost-reference">{r.reference}</p>
        {first && <small>首次观察（按有效记录）</small>}
        <p className="cost-note">{r.note}</p>
        {r.type === "human_effort" && (
          <p>
            {date(r.startedAt || "")} 至 {date(r.endedAt || "")} · 已登记{" "}
            {seconds(r.durationSeconds || 0)}
          </p>
        )}
        {r.verificationEnvironment && (
          <small>预览运行环境：{r.verificationEnvironment}</small>
        )}
        {r.commit && <p className="mono cost-reference">源码提交 {r.commit}</p>}
        {r.releaseId && (
          <small className="cost-reference">关联版本 {r.releaseId}</small>
        )}
        {r.proofArtifactId && (
          <>
            <a
              className="text-button"
              href={
                "/api/v1/artifacts/" +
                encodeURIComponent(r.proofArtifactId) +
                "/content"
              }
              target="_blank"
              rel="noopener noreferrer"
            >
              查看报告附件
            </a>
            <small className="mono cost-reference">{r.proofDigest}</small>
          </>
        )}
        {r.replacesMeasurementId && (
          <small className="cost-reference">
            更正自 {r.replacesMeasurementId}
          </small>
        )}
        {r.issues.map((issue) => (
          <p key={issue} className="blocked-reason">
            待核对，未计入统计：{issue}
          </p>
        ))}
        {r.status === "voided" ? (
          <div className="cost-void-note">
            <small>
              {date(r.voidedAt || "")} · {r.voidedBy}
            </small>
            <p>{r.voidReason}</p>
          </div>
        ) : (
          canObserve &&
          (!historical || r.type === "production_defect") && (
            <div className="cost-entry-actions">
              <Button secondary onClick={() => voidRecord(r)}>
                作废观测
              </Button>
            </div>
          )
        )}
      </article>
    );
  }

  return (
    <>
      <SectionHeading
        eyebrow="DELIVERY METRICS"
        title="交付效能与观测依据"
        description="围绕需求版本查看已登记的业务结果、投入和渠道观测，保留样本与缺失范围。"
        action={
          <Button secondary onClick={() => setRefresh((v) => v + 1)}>
            <RefreshCw size={15} />
            刷新效能
          </Button>
        }
      />
      <p className="cost-context">
        {w.goal
          ? "当前观测：" + w.goal.title
          : "当前观测：项目各目标的当前需求版本"}
      </p>
      {!!w.goal && (
        <label className="field">
          <span>需求版本</span>
          <select
            aria-label="效能需求版本"
            value={selectedRevision}
            onChange={(e) => setRevision({ goalId, value: e.target.value })}
          >
            <option value="">当前版本 v{w.goal.revision}</option>
            {revisions
              .filter((n) => n !== w.goal?.revision)
              .map((n) => (
                <option key={n} value={n}>
                  历史版本 v{n}
                </option>
              ))}
          </select>
        </label>
      )}
      {historical && <p className="form-note">历史观测不作为当前版本验收</p>}
      <div className="button-group measurement-actions">
        <Button
          secondary
          disabled={!canWrite}
          onClick={() => record("preview")}
        >
          确认可运行预览
        </Button>
        <Button
          secondary
          disabled={!canWrite}
          onClick={() => record("business_acceptance")}
        >
          登记业务验收
        </Button>
        <Button
          secondary
          disabled={!canWrite}
          onClick={() => record("human_effort")}
        >
          登记本人投入
        </Button>
        <Button
          secondary
          disabled={!canObserve}
          onClick={() => record("production_defect")}
        >
          登记上线缺陷
        </Button>
        <ReleaseFeedback revision={observationRevision} canWrite={canObserve} />
      </div>
      {!current || current.loading ? (
        <p role="status">正在加载交付效能…</p>
      ) : current.error ? (
        <p role="alert" className="blocked-reason">
          {current.error}
        </p>
      ) : (
        report && (
          <>
            <dl className="cost-summary">
              <div role="group" aria-label="需求到预览">
                <dt>需求到预览</dt>
                <dd>{seconds(report.meanPreviewSeconds)}</dd>
                <small>
                  {report.previewSamples} / {report.requirements}{" "}
                  个需求版本已观察 · 平台首次确认均值
                </small>
              </div>
              <div role="group" aria-label="首次业务验收">
                <dt>首次业务验收</dt>
                <dd>{rate(report.firstPassRate)}</dd>
                <small>
                  {report.acceptanceSamples} / {report.requirements}{" "}
                  个需求版本已观察 · 首次通过 {report.firstPassCount}
                </small>
              </div>
              <div role="group" aria-label="人工投入">
                <dt>已登记人工投入</dt>
                <dd>{seconds(report.humanSeconds)}</dd>
                <small>{report.effortSamples} 个有效区间 · 单位人秒</small>
              </div>
              <div role="group" aria-label="发布执行失败">
                <dt>发布执行失败</dt>
                <dd>{rate(report.publishExecutionFailureRate)}</dd>
                <small>
                  {report.failedPublishExecutions} / {report.publishExecutions}{" "}
                  次已结束提交操作 · 排除状态轮询
                </small>
              </div>
              <div role="group" aria-label="上线缺陷">
                <dt>已登记上线缺陷</dt>
                <dd>{report.reportedProductionDefects}</dd>
                <small>登记数不能证明完整观察窗口内没有缺陷</small>
              </div>
              <div role="group" aria-label="渠道等待">
                <dt>已观测渠道等待</dt>
                <dd>{report.observedChannelWaitSeconds.toFixed(1)} 渠道秒</dd>
                <small>
                  {report.channelHistoryComplete
                    ? "所选候选已采集状态历史"
                    : "历史未完整采集"}{" "}
                  · 并行渠道分别累计
                </small>
              </div>
              <div role="group" aria-label="完整交付成本">
                <dt>成功交付完整成本</dt>
                <dd>待完整核算</dd>
                <small>已登记费用查看「交付成本」</small>
              </div>
              <div role="group" aria-label="人工对照基线">
                <dt>人工研发对照基线</dt>
                <dd>待采集</dd>
                <small>尚不能计算效率提升比例</small>
              </div>
            </dl>
            <section className="cost-block" aria-label="指标口径与缺失">
              <h3>指标口径与缺失</h3>
              {report.gaps.map((gap) => (
                <p key={gap} className="cost-context">
                  {gap}
                </p>
              ))}
            </section>
            <section className="cost-block" aria-label="有效观测">
              <h3>有效观测与来源</h3>
              {!report.observations.some((r) => r.status !== "voided") && (
                <p className="muted">
                  尚无人工观测，技术测试记录不会自动转换为业务验收。
                </p>
              )}
              <div className="cost-entries">
                {report.observations
                  .filter((r) => r.status !== "voided")
                  .map(row)}
              </div>
            </section>
            {report.observations.some((r) => r.status === "voided") && (
              <details className="cost-history">
                <summary>
                  已作废观测（
                  {
                    report.observations.filter((r) => r.status === "voided")
                      .length
                  }
                  ）
                </summary>
                <div className="cost-entries">
                  {report.observations
                    .filter((r) => r.status === "voided")
                    .map(row)}
                </div>
              </details>
            )}
          </>
        )
      )}
    </>
  );
}
