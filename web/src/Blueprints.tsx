import { Boxes, Plus } from "lucide-react";
import { useEffect, useState } from "react";
import { api, type Data, type Resource } from "./api";
import { useWorkspace } from "./context";
import { Button, Form } from "./ui";

export function currentBlueprints(items: Resource[], goal: Resource) {
  return items
    .filter(
      (r) =>
        r.kind === "blueprint" &&
        r.goalId === goal.id &&
        r.data.requirementRevision === goal.revision &&
        r.status === "proposed",
    )
    .sort((a, b) => b.revision - a.revision);
}

export function BlueprintSummary({
  snapshot,
  initialize = false,
}: {
  snapshot?: Data;
  initialize?: boolean;
}) {
  if (!snapshot?.data) return null;
  const data = snapshot.data;
  return (
    <section className="design-summary" aria-label="方案采用的蓝图">
      <h4>
        蓝图 v{snapshot.revision} · {data.applicationName}
      </h4>
      <p>
        Flutter + Go · {data.mode === "booking" ? "服务预约" : "客户反馈"} ·{" "}
        {data.organization}
      </p>
      <p>
        {initialize
          ? "批准后创建源码初始化工作项。已有源码会阻止初始化。"
          : "作为后续开发依据，保留已有源码。"}
      </p>
      <p className="muted">原生构建、独立验收与正式发布分别执行。</p>
    </section>
  );
}

function BlueprintForm({ descriptor }: { descriptor: Data }) {
  const w = useWorkspace();
  const [captured] = useState(() => ({
    goal: w.goal!,
    descriptor,
    key: crypto.randomUUID(),
  }));
  return (
    <>
      <div className="blueprint-form-intro">
        <p>
          {captured.goal.title} · 需求 v{captured.goal.revision}
        </p>
        <p className="muted">
          保存应用标识、模板版本和源码。选入方案并获批准后，执行节点才会初始化应用。
        </p>
      </div>
      <Form
        submit="保存蓝图提案"
        fields={[
          {
            name: "applicationName",
            label: "应用名",
            required: true,
            placeholder: "customer_portal",
            hint: "2–50 个小写字母、数字或下划线，以字母开头。",
          },
          {
            name: "organization",
            label: "应用命名空间",
            required: true,
            placeholder: "com.company",
            hint: "使用反向域名，例如 com.company。",
          },
          {
            name: "mode",
            label: "参考业务",
            type: "select",
            defaultValue: "feedback",
            options: { feedback: "客户反馈", booking: "服务预约" },
          },
          {
            name: "repositoryId",
            label: "初始化仓库",
            type: "select",
            options: {
              "": "使用唯一空仓库；无仓库时在批准后创建",
              ...Object.fromEntries(
                w.select("repository").map((r) => [r.id, r.title]),
              ),
            },
          },
          {
            name: "reason",
            label: "选用原因",
            type: "textarea",
            required: true,
          },
        ]}
        onSubmit={async (values) => {
          try {
            await w.run("blueprints.propose", {
              ...values,
              goalId: captured.goal.id,
              expectedRequirementRevision: captured.goal.revision,
              blueprintId: captured.descriptor.id,
              expectedCatalogDigest: captured.descriptor.digest,
              key: captured.key,
            });
            w.close();
          } finally {
            await w.refresh();
          }
        }}
      />
    </>
  );
}

export function Blueprints() {
  const w = useWorkspace();
  const [catalog, setCatalog] = useState<Data[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    setLoading(true);
    setCatalog([]);
    setError("");
    api<Data[]>("projects/" + w.project.id + "/blueprints")
      .then((value) => {
        if (active) setCatalog(value);
      })
      .catch((e) => {
        if (active) setError(e.message);
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [w.project.id]);
  const goal = w.goal;
  if (!goal) return null;
  const proposals = currentBlueprints(w.items, goal);
  const current = w.items.find((r) => r.id === goal.data.activePlanId)?.data
    .blueprint;
  const canPropose =
    ["human", "agent"].includes(w.actor.kind) &&
    w.can("product", "architect", "developer") &&
    goal.data.approvedRevision === goal.revision;
  return (
    <section aria-label="应用蓝图">
      <div className="content-heading">
        <h3>
          <Boxes size={17} /> 应用蓝图
        </h3>
        <Button
          secondary
          disabled={!canPropose || loading || !catalog.length}
          onClick={() =>
            w.open("选用应用蓝图", <BlueprintForm descriptor={catalog[0]} />)
          }
        >
          <Plus size={15} /> 选用蓝图
        </Button>
      </div>
      {loading ? (
        <p className="muted">读取蓝图目录…</p>
      ) : error ? (
        <p role="alert">{error}</p>
      ) : !catalog.length ? (
        <p className="inline-empty">管理员尚未启用应用蓝图目录。</p>
      ) : (
        <p className="muted">
          Flutter + Go · 客户反馈 /
          服务预约。后端为单实例参考实现，生产业务与容量仍需验收。
        </p>
      )}
      {proposals.map((proposal) => (
        <details className="clarification-history" key={proposal.id}>
          <summary>
            蓝图 v{proposal.revision} · {proposal.data.applicationName} ·{" "}
            {current?.id === proposal.id &&
            goal.data.approvedRevision === goal.revision
              ? "当前批准蓝图"
              : "待方案确认"}
          </summary>
          <BlueprintSummary snapshot={proposal} />
          <p>{proposal.data.reason}</p>
          <p className="muted">
            来源 {proposal.data.blueprintId} · {proposal.data.fileCount}{" "}
            个源码文件
          </p>
          <details>
            <summary>来源与归档摘要</summary>
            <p className="mono blueprint-digest">
              {proposal.data.catalogDigest}
            </p>
            <p className="mono blueprint-digest">{proposal.data.digest}</p>
          </details>
        </details>
      ))}
    </section>
  );
}
