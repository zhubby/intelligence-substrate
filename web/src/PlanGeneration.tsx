import { Sparkles } from "lucide-react";
import { useState } from "react";
import { date, money, post } from "./api";
import { useWorkspace } from "./context";
import { PlanForm } from "./forms";
import { Button, Confirm } from "./ui";
import { currentDesigns, DesignSummary } from "./Designs";
import { BlueprintSummary, currentBlueprints } from "./Blueprints";
import { currentReleaseIssues } from "./ReleaseIssues";

export function PlanGeneration() {
  const w = useWorkspace();
  const goal = w.goal;
  const [selection, setSelection] = useState("");
  const [blueprintSelection, setBlueprintSelection] = useState("");
  const [initializeBlueprint, setInitializeBlueprint] = useState(true);
  if (!goal) return null;
  const designs = currentDesigns(w.items, goal);
  const selected = designs.find((x) => x.id === selection);
  const blueprints = currentBlueprints(w.items, goal);
  const blueprint = blueprints.find((x) => x.id === blueprintSelection);
  const issues = currentReleaseIssues(w.items, goal);
  const unconfirmed = issues.some(
    (issue) =>
      !issue.data.triage ||
      issue.data.triage.requirementRevision !== goal.revision,
  );
  const canPropose =
    ["human", "agent"].includes(w.actor.kind) &&
    w.can("product", "architect", "developer") &&
    goal.data.approvedRevision === goal.revision &&
    !unconfirmed &&
    !issues.some((issue) => issue.data.triage?.strategy === "store-text");
  const requests = w
    .select("plan_generation")
    .filter((r) => r.goalId === goal.id)
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  function generate() {
    const captured = goal!;
    const key = crypto.randomUUID();
    w.open(
      "生成交付方案",
      <Confirm
        label="生成交付方案"
        description={
          <>
            <p>
              {captured.title} · 需求 v{captured.revision}
            </p>
            <p>
              根据已批准需求、人工答复、项目知识和工程资源生成待审方案。此次调用计入目标模型预算。
            </p>
            <p>
              方案需由架构负责人单独批准。请求未完成时，请先核对生成与费用记录，再决定是否重新发起。
            </p>
            {selected && <DesignSummary snapshot={selected} />}
            <BlueprintSummary
              snapshot={blueprint}
              initialize={initializeBlueprint}
            />
          </>
        }
        onConfirm={async () => {
          try {
            await post("plans/generate", {
              goalId: captured.id,
              expectedRequirementRevision: captured.revision,
              key,
              ...(blueprint
                ? {
                    blueprintId: blueprint.id,
                    expectedBlueprintBinding: blueprint.data.binding,
                    initializeBlueprint,
                  }
                : {}),
              ...(selected
                ? {
                    designId: selected.id,
                    expectedDesignBinding: selected.data.binding,
                  }
                : {}),
            });
            w.close();
          } finally {
            await w.refresh();
          }
        }}
      />,
    );
  }
  return (
    <section aria-label="方案生成与记录">
      <div className="content-heading">
        <h3>动态交付方案</h3>
        <div className="button-group">
          <Button
            secondary
            disabled={!canPropose}
            onClick={() =>
              w.open(
                "提交交付方案",
                <PlanForm
                  design={selected}
                  blueprint={blueprint}
                  initializeBlueprint={initializeBlueprint}
                />,
              )
            }
          >
            人工编制
          </Button>
          <Button disabled={!canPropose} onClick={generate}>
            <Sparkles size={15} />
            生成方案
          </Button>
        </div>
      </div>
      {issues.length > 0 && (
        <p className={unconfirmed ? "blocked-reason" : "form-note"}>
          {unconfirmed
            ? "有拒审问题尚待确认修复要求，请先到构建与发布处理。"
            : issues.some(
                  (issue) => issue.data.triage?.strategy === "store-text",
                )
              ? "文字资料修订请在构建与发布中单独审批；如需修改代码，请先将修复方式改为新构建。"
              : `本次规划会包含 ${issues.length} 项已确认的拒审修复要求。`}
        </p>
      )}
      <label className="field design-selection">
        <span>规划采用的设计</span>
        <select
          aria-label="规划采用的设计"
          value={selected?.id || ""}
          onChange={(event) => setSelection(event.target.value)}
          disabled={!canPropose}
        >
          <option value="">
            {goal.data.activePlanId
              ? "沿用当前批准设计（如有）"
              : "暂不关联设计"}
          </option>
          {designs.map((design) => (
            <option key={design.id} value={design.id}>
              设计 v{design.revision} · {design.title}
            </option>
          ))}
        </select>
        <small>
          选择具体版本后，方案会保留对应设计与原型；批准新方案后更新执行依据。
        </small>
      </label>
      <label className="field design-selection">
        <span>规划采用的蓝图</span>
        <select
          aria-label="规划采用的蓝图"
          value={blueprint?.id || ""}
          disabled={!canPropose}
          onChange={(event) => {
            setBlueprintSelection(event.target.value);
            setInitializeBlueprint(true);
          }}
        >
          <option value="">
            {goal.data.activePlanId
              ? "沿用当前批准蓝图（如有）"
              : "暂不关联蓝图"}
          </option>
          {blueprints.map((item) => (
            <option key={item.id} value={item.id}>
              蓝图 v{item.revision} · {item.data.applicationName}
            </option>
          ))}
        </select>
        <small>方案会保留对应应用标识和源码版本。</small>
      </label>
      {blueprint && (
        <label className="checkbox-field">
          <input
            type="checkbox"
            checked={initializeBlueprint}
            disabled={!canPropose}
            onChange={(event) => setInitializeBlueprint(event.target.checked)}
          />
          <span>从蓝图初始化空应用源码</span>
        </label>
      )}
      {requests.length > 0 && (
        <details className="clarification-history">
          <summary>方案生成记录（{requests.length}）</summary>
          <div className="clarification-generations">
            {requests.map((r) => {
              const charges = w
                .select("charge")
                .filter((c) => c.data.planRequestId === r.id);
              const charge = charges.length === 1 ? charges[0] : undefined;
              const plan = w.items.find((p) => p.id === r.data.resultId);
              return (
                <div key={r.id}>
                  <strong>
                    {r.status === "succeeded"
                      ? "方案已生成"
                      : r.status === "failed"
                        ? "生成未完成"
                        : "待返回或待核对"}
                  </strong>
                  <small>
                    需求 v{r.data.requirementRevision} ·{" "}
                    {r.data.requestedName || r.data.requestedBy} ·{" "}
                    {date(r.createdAt)}
                  </small>
                  {plan && <p>方案 v{plan.revision}</p>}
                  <p>
                    {charges.length > 1
                      ? "费用关联异常，需要核对"
                      : !charge
                        ? "尚无关联费用记录"
                        : charge.status === "settled"
                          ? "已记录模型费用 " + money(charge.data.chargedCents)
                          : "模型费用待核对，预留 " +
                            money(charge.data.reservedCents)}
                  </p>
                  {r.status !== "succeeded" && (
                    <p>请先核对记录与费用；重新发起会创建新的模型请求。</p>
                  )}
                  <details>
                    <summary>追溯信息</summary>
                    <code>{r.id}</code>
                    {charge && <code>{charge.id}</code>}
                    {plan && <code>{plan.id}</code>}
                  </details>
                </div>
              );
            })}
          </div>
        </details>
      )}
    </section>
  );
}
