import { useEffect, useState } from "react";
import { Plus, RefreshCw } from "lucide-react";
import { api, date, money, type Resource } from "./api";
import { useWorkspace } from "./context";
import { Badge, Button, Form, SectionHeading } from "./ui";

const categories: Record<string, string> = {
  build: "构建",
  device: "设备",
  storage: "存储",
  channel: "渠道",
  labor: "人工",
  other: "其他",
};
interface CostReport {
  modelBudgetCents: number;
  modelEstimatedCents: number;
  modelReconciledCents: number;
  modelPendingCents: number;
  externalRecordedCents: number;
  recordedTotalCents: number;
  exposureCents: number;
  externalByCategory: Record<string, number>;
  unreportedCategories: string[];
  unmeasurable: { resourceId: string; reason: string }[];
  charges: Resource[];
  expenses: Resource[];
}
function validCents(value: unknown): value is number {
  return (
    typeof value === "number" &&
    Number.isInteger(value) &&
    value >= 0 &&
    value <= 100000000
  );
}
function displayAmount(value: unknown) {
  return validCents(value) ? money(value) : "金额待核对";
}
function usdCents(value: number) {
  const rounded = Math.round(value * 100);
  if (
    !Number.isFinite(value) ||
    !validCents(rounded) ||
    Math.abs(value * 100 - rounded) > 0.0000001
  )
    throw new Error("USD 金额必须在 0–1000000 之间，最多两位小数。");
  return rounded;
}
function localDateTime() {
  const now = new Date();
  return new Date(now.getTime() - now.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 16);
}

export function DeliveryCosts() {
  const w = useWorkspace();
  const goalId = w.goal?.id || "";
  const scope = w.project.id + "/" + goalId;
  const [snapshot, setSnapshot] = useState<{
    scope: string;
    data: CostReport;
  }>();
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  const report = snapshot?.scope === scope ? snapshot.data : undefined;
  const canWrite = w.actor.kind === "human" && w.can("owner");
  useEffect(() => {
    let current = true;
    setError("");
    api<CostReport>(
      "projects/" +
        encodeURIComponent(w.project.id) +
        "/costs" +
        (goalId ? "?goalId=" + encodeURIComponent(goalId) : ""),
    )
      .then((data) => {
        if (current) setSnapshot({ scope, data });
      })
      .catch((e) => {
        if (current) {
          setSnapshot(undefined);
          setError((e as Error).message);
        }
      });
    return () => {
      current = false;
    };
  }, [scope, w.project.id, goalId, w.items, refresh]);

  function record() {
    if (!goalId) return;
    const key = crypto.randomUUID();
    w.open(
      "登记外部费用",
      <Form
        fields={[
          { name: "title", label: "费用名称", required: true },
          {
            name: "category",
            label: "费用类别",
            type: "select",
            required: true,
            options: categories,
          },
          {
            name: "amount",
            label: "登记金额（USD）",
            type: "number",
            required: true,
            hint: "可明确登记零费用；未知金额请保留为尚未记录。",
          },
          {
            name: "incurredAt",
            label: "发生时间",
            type: "datetime-local",
            required: true,
            defaultValue: localDateTime(),
          },
          {
            name: "reference",
            label: "费用来源标识",
            required: true,
            hint: "填写稳定的账单行、工单或分摊编号，项目内不能重复计入。",
          },
          { name: "note", label: "登记依据", type: "textarea", required: true },
          {
            name: "proofArtifactId",
            label: "依据附件（可选）",
            type: "select",
            options: Object.fromEntries(
              w
                .select("artifact")
                .filter((a) => a.goalId === goalId && a.data.type === "report")
                .map((a) => [a.id, a.title]),
            ),
          },
        ]}
        submit="登记费用"
        onSubmit={async (v) => {
          await w.run("expenses.record", {
            goalId,
            key,
            title: v.title,
            category: v.category,
            currency: "USD",
            amountCents: usdCents(v.amount),
            incurredAt: new Date(v.incurredAt).toISOString(),
            reference: v.reference,
            note: v.note,
            proofArtifactId: v.proofArtifactId,
          });
          w.close();
        }}
      >
        <p className="form-note">
          关联当前目标与需求版本。登记保留金额和来源；更正时先作废原记录，再登记新金额。
        </p>
      </Form>,
    );
  }
  function voidExpense(expense: Resource) {
    const key = crypto.randomUUID();
    w.open(
      "作废费用记录",
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
          await w.run("expenses.void", {
            id: expense.id,
            key,
            reason: v.reason,
          });
          w.close();
        }}
      >
        <p className="form-note">
          {expense.title} · {displayAmount(expense.data.amountCents)}
          。原金额、来源和作废原因将保留，作废后从成本小计中移除。
        </p>
      </Form>,
    );
  }
  function reconcile(charge: Resource) {
    const amount = charge.data.chargedCents ?? charge.data.reservedCents;
    w.open(
      "核对实际模型费用",
      <Form
        fields={[
          {
            name: "amount",
            label: "实际费用（USD）",
            type: "number",
            required: true,
            defaultValue: validCents(amount) ? amount / 100 : undefined,
          },
          {
            name: "reason",
            label: "供应商账单与核对依据",
            type: "textarea",
            required: true,
          },
        ]}
        submit="确认费用"
        onSubmit={async (v) => {
          await w.run("charges.reconcile", {
            id: charge.id,
            chargedCents: usdCents(v.amount),
            reason: v.reason,
          });
          w.close();
        }}
      >
        <p className="form-note">
          核对会按实际金额更新模型预算消耗。请保留供应商记录与分摊依据。
        </p>
      </Form>,
    );
  }
  function expenseRow(expense: Resource) {
    const voided = expense.status === "voided";
    return (
      <article
        className="cost-entry"
        aria-label={"费用记录 " + expense.id}
        key={expense.id}
      >
        <div className="cost-entry-heading">
          <div>
            <strong>{expense.title}</strong>
            <small>
              {categories[expense.data.category] || "类别待核对"} ·{" "}
              {date(expense.data.incurredAt)} · 需求 v
              {expense.data.requirementRevision}
            </small>
          </div>
          <span className="cost-amount">
            {displayAmount(expense.data.amountCents)}
          </span>
          <Badge status={expense.status} />
        </div>
        <p className="mono cost-reference">{expense.data.reference}</p>
        <p className="cost-note">{expense.data.note}</p>
        <small>
          登记人：{expense.data.recordedName || expense.data.recordedBy} ·{" "}
          {date(expense.createdAt)}
        </small>
        {expense.data.replacesExpenseId && (
          <small className="cost-reference">
            更正自 {expense.data.replacesExpenseId}
          </small>
        )}
        {expense.data.proofArtifactId && (
          <a
            className="text-button"
            href={
              "/api/v1/artifacts/" +
              encodeURIComponent(expense.data.proofArtifactId) +
              "/content"
            }
            target="_blank"
            rel="noopener noreferrer"
          >
            查看依据附件
          </a>
        )}
        {voided ? (
          <div className="cost-void-note">
            <small>
              {expense.data.voidedName || expense.data.voidedBy} ·{" "}
              {date(expense.data.voidedAt)}
            </small>
            <p className="cost-note">{expense.data.voidReason}</p>
          </div>
        ) : (
          <div className="cost-entry-actions">
            <Button
              secondary
              disabled={!canWrite || expense.status !== "recorded"}
              onClick={() => voidExpense(expense)}
            >
              作废费用
            </Button>
          </div>
        )}
      </article>
    );
  }
  const expenses = [...(report?.expenses || [])].sort(
    (a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt),
  );
  const active = expenses.filter((e) => e.status !== "voided");
  const voided = expenses.filter((e) => e.status === "voided");
  return (
    <div className="delivery-costs">
      <SectionHeading
        title="交付成本与来源"
        description={
          (w.goal?.title || "当前项目全部目标") +
          " · 已记录小计包含模型估算和人工登记。完整成本仍需核对企业账单与工时。"
        }
        action={
          <div className="button-group">
            <Button secondary onClick={() => setRefresh((n) => n + 1)}>
              <RefreshCw size={14} />
              刷新费用
            </Button>
            <Button disabled={!canWrite || !goalId} onClick={record}>
              <Plus size={15} />
              登记外部费用
            </Button>
          </div>
        }
      />
      {!goalId && <p className="form-note">选择交付目标后可登记费用。</p>}
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      {!report && !error && <p role="status">正在读取费用记录…</p>}
      {report && (
        <>
          <section aria-label="成本概览">
            <dl className="cost-summary">
              <div>
                <dt>已记录成本小计</dt>
                <dd>{money(report.recordedTotalCents)}</dd>
                <small>含模型估算与人工登记</small>
              </div>
              <div>
                <dt>模型待核对金额</dt>
                <dd>{money(report.modelPendingCents)}</dd>
                <small>含待核对的已预留金额</small>
              </div>
              <div>
                <dt>已登记外部费用</dt>
                <dd>{money(report.externalRecordedCents)}</dd>
                <small>作废记录已排除</small>
              </div>
            </dl>
            <p className="cost-context">
              模型预算 {money(report.modelBudgetCents)} · 含待核对金额的小计{" "}
              {money(report.exposureCents)}
            </p>
          </section>
          <section className="cost-block" aria-label="费用类别">
            <h3>外部费用类别</h3>
            <dl className="cost-breakdown">
              {Object.entries(categories).map(([key, label]) => (
                <div key={key}>
                  <dt>{label}</dt>
                  <dd>
                    {Object.hasOwn(report.externalByCategory, key)
                      ? money(report.externalByCategory[key])
                      : "尚未记录"}
                  </dd>
                </div>
              ))}
            </dl>
            <p className="form-note" role="status">
              {report.unreportedCategories.length
                ? "尚未记录：" +
                  report.unreportedCategories
                    .map((c) => categories[c] || c)
                    .join("、")
                : "五类费用均有登记，完整金额仍须核对账单与工时。"}
            </p>
          </section>
          {report.unmeasurable.length > 0 && (
            <div className="form-error" role="alert">
              <strong>
                有 {report.unmeasurable.length} 条记录无法核算，尚未计入小计。
              </strong>
              {report.unmeasurable.map((p) => (
                <p className="cost-reference" key={p.resourceId}>
                  {p.resourceId} · {p.reason}
                </p>
              ))}
            </div>
          )}
          <section className="cost-block" aria-label="模型费用来源">
            <h3>模型费用来源</h3>
            <p className="form-note">
              配置价格估算 {money(report.modelEstimatedCents)} · 人工核对{" "}
              {money(report.modelReconciledCents)}
            </p>
            <div className="cost-entries">
              {report.charges.map((c) => (
                <article
                  className="cost-entry"
                  aria-label={"模型费用 " + c.id}
                  key={c.id}
                >
                  <div className="cost-entry-heading">
                    <div>
                      <strong>
                        {c.data.providerModel || c.title || "模型调用"}
                      </strong>
                      <small>
                        {date(c.createdAt)} · {c.data.runId || "方案或独立调用"}
                      </small>
                    </div>
                    <span className="cost-amount">
                      {displayAmount(
                        c.status === "reserved"
                          ? c.data.reservedCents
                          : (c.data.chargedCents ?? c.data.reservedCents),
                      )}
                    </span>
                    <Badge status={c.status} />
                  </div>
                  <small>
                    {c.status === "settled"
                      ? c.data.reconciledBy
                        ? "人工核对金额"
                        : "配置价格估算"
                      : "待核对预留"}
                  </small>
                  {c.data.reason && (
                    <p className="cost-note">{c.data.reason}</p>
                  )}
                  {["reserved", "uncertain"].includes(c.status) && (
                    <div className="cost-entry-actions">
                      <Button
                        secondary
                        disabled={!canWrite}
                        onClick={() => reconcile(c)}
                      >
                        核对模型费用
                      </Button>
                    </div>
                  )}
                </article>
              ))}
            </div>
            {!report.charges.length && (
              <p className="inline-empty">尚无模型调用费用记录</p>
            )}
          </section>
          <section className="cost-block" aria-label="外部费用来源">
            <h3>外部费用来源</h3>
            <p className="form-note">
              人工登记的费用与依据。模型预算准入及独立交付验收分别管理。
            </p>
            <div className="cost-entries">{active.map(expenseRow)}</div>
            {!active.length && (
              <p className="inline-empty">尚无活动外部费用记录</p>
            )}
            {voided.length > 0 && (
              <details className="cost-history">
                <summary>已作废记录（{voided.length}）</summary>
                <div className="cost-entries">{voided.map(expenseRow)}</div>
              </details>
            )}
          </section>
        </>
      )}
    </div>
  );
}
