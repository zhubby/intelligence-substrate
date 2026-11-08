import { Plus } from "lucide-react";
import { date, targets, type Data, type Resource } from "./api";
import { useWorkspace } from "./context";
import { Button, Form } from "./ui";

export function DesignSummary({ snapshot }: { snapshot?: Data }) {
  if (!snapshot) return null;
  const data = snapshot.data;
  return (
    <section
      className="design-summary"
      aria-label={"方案关联设计 v" + snapshot.revision}
    >
      <h4>
        设计 v{snapshot.revision} · {snapshot.title}
      </h4>
      <p>{data.summary}</p>
      <div className="design-decisions">
        <strong>关键设计决定</strong>
        <p>{data.decisions}</p>
      </div>
      <small>
        需求 v{data.requirementRevision} · 验收项 {data.criteria.join("、")}
      </small>
      <div className="design-prototypes">
        {data.prototypes.map((prototype: Data) => (
          <div key={prototype.artifactId}>
            <a
              href={"/api/v1/artifacts/" + prototype.artifactId + "/preview"}
              target="_blank"
              rel="noreferrer"
            >
              预览 {prototype.name}
            </a>
            <small>
              {targets[prototype.target] || prototype.target} · 源提交{" "}
              {prototype.commit}
            </small>
            <details>
              <summary>原型追溯</summary>
              <code>{prototype.digest}</code>
              <code>{prototype.artifactId}</code>
            </details>
          </div>
        ))}
      </div>
    </section>
  );
}

export function currentDesigns(items: Resource[], goal?: Resource) {
  return items
    .filter(
      (x) =>
        x.kind === "design" &&
        x.goalId === goal?.id &&
        x.status === "proposed" &&
        x.data.requirementRevision === goal?.revision,
    )
    .sort((a, b) => b.revision - a.revision);
}

export function Designs() {
  const w = useWorkspace();
  const goal = w.goal;
  if (!goal) return null;
  const records = w
    .select("design")
    .filter((x) => x.goalId === goal.id)
    .sort((a, b) => b.revision - a.revision);
  const activePlan = w.items.find(
    (x) => x.id === goal.data.activePlanId && x.status === "active",
  );
  const activeDesign = activePlan?.data.design?.id;
  const canPropose =
    ["human", "agent"].includes(w.actor.kind) &&
    w.can("product", "architect", "developer", "design") &&
    goal.data.approvedRevision === goal.revision;
  function propose() {
    const captured = goal!;
    const key = crypto.randomUUID();
    const prototypes = w
      .select("artifact")
      .filter(
        (x) =>
          x.goalId === captured.id &&
          x.data.requirementRevision === captured.revision &&
          x.data.type === "prototype" &&
          x.data.size <= 2 * 1024 * 1024,
      );
    w.open(
      "提交设计提案",
      <Form
        fields={[
          { name: "title", label: "设计名称", required: true },
          {
            name: "summary",
            label: "设计说明",
            type: "textarea",
            required: true,
          },
          {
            name: "decisions",
            label: "关键设计决定",
            type: "textarea",
            required: true,
            hint: "说明主要交互、架构选择和取舍；业务范围变化请通过需求变更记录。",
          },
          {
            name: "reason",
            label: "设计原因",
            type: "textarea",
            required: true,
          },
          ...captured.data.criteria.map((c: Data) => ({
            name: "criterion:" + c.id,
            label: c.text,
            type: "checkbox",
            defaultValue: true,
          })),
          ...prototypes.map((x) => ({
            name: "prototype:" + x.id,
            label: x.title,
            type: "checkbox",
          })),
        ]}
        submit="保存设计提案"
        onSubmit={async (values) => {
          try {
            await w.run("designs.propose", {
              goalId: captured.id,
              expectedRequirementRevision: captured.revision,
              key,
              title: values.title,
              summary: values.summary,
              decisions: values.decisions,
              reason: values.reason,
              criteria: captured.data.criteria
                .filter((c: Data) => values["criterion:" + c.id])
                .map((c: Data) => c.id),
              prototypeArtifactIds: prototypes
                .filter((x) => values["prototype:" + x.id])
                .map((x) => x.id),
            });
            w.close();
          } catch (error) {
            await w.refresh();
            throw error;
          }
        }}
      >
        <p className="form-note">
          {captured.title} · 需求 v{captured.revision}
          。关联验收项和原型随设计版本保存；选择设计并批准交付方案后生效。
        </p>
        {!prototypes.length && (
          <p className="form-note">
            暂无可关联原型。可在代码与构建中上传独立 HTML
            原型，再提交新的设计版本。
          </p>
        )}
      </Form>,
    );
  }
  return (
    <section className="designs" aria-label="设计与原型">
      <div className="content-heading design-heading">
        <div>
          <h3>设计与原型</h3>
          <p className="muted">
            把交互与架构决定留在版本中，随交付方案一并确认。
          </p>
        </div>
        <Button secondary disabled={!canPropose} onClick={propose}>
          <Plus size={14} />
          提交设计
        </Button>
      </div>
      {!records.length && <p className="inline-empty">还没有设计记录</p>}
      {records.map((record) => (
        <article
          className="design-record"
          key={record.id}
          aria-label={"设计记录 v" + record.revision}
        >
          <div className="content-heading">
            <strong>设计 v{record.revision}</strong>
            <span className="clarification-state answered">
              {record.status === "stale" ||
              record.data.requirementRevision !== goal.revision
                ? "历史设计"
                : record.id === activeDesign
                  ? "当前批准设计"
                  : "待方案确认"}
            </span>
          </div>
          <DesignSummary snapshot={record} />
          <small>
            {record.data.authorName || record.data.authorId} ·{" "}
            {date(record.createdAt)}
          </small>
          <p className="form-note">设计原因 · {record.data.reason}</p>
        </article>
      ))}
    </section>
  );
}
