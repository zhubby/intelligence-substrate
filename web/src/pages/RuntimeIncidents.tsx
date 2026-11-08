import { date, type Resource } from "../api";
import { useWorkspace } from "../context";
import { Badge, Button, Form } from "../ui";

export function RuntimeIncidents({ incidents }: { incidents: Resource[] }) {
  const w = useWorkspace();
  const sorted = [...incidents].sort((a, b) =>
    String(b.data.openedAt).localeCompare(String(a.data.openedAt)),
  );
  const active = sorted.filter((x) => x.status === "open");
  const resolved = sorted.filter((x) => x.status === "resolved");
  if (!incidents.length) return null;

  function takeOwnership(incident: Resource) {
    // Keep one key for this form's retries, including a lost HTTP response.
    const key = crypto.randomUUID();
    w.open(
      "确认接手运行事件",
      <Form
        fields={[
          { name: "note", label: "处理说明", type: "textarea", required: true },
        ]}
        submit="确认接手"
        onSubmit={async (v) => {
          await w.run("runtime.incident.acknowledge", {
            id: incident.id,
            key,
            note: v.note,
          });
          w.close();
        }}
      >
        <p className="form-note">
          {incident.title}。确认后记录你负责跟进；事件根据可信检查结果恢复。
        </p>
      </Form>,
    );
  }

  function row(incident: Resource) {
    const signal = incident.data.type === "monitor_signal_lost";
    const recovered = incident.status === "resolved";
    return (
      <article
        className="runtime-incident"
        aria-label={"运行事件 " + incident.id}
        key={incident.id}
      >
        <Badge
          status={recovered ? "resolved" : signal ? "unknown" : "unhealthy"}
        />
        <div className="runtime-incident-content">
          <strong>{incident.title}</strong>
          <small>
            {signal ? "监测失联" : "应用故障"} · 开始于{" "}
            {date(incident.data.openedAt)}
          </small>
          {signal && !recovered && (
            <p className="form-note">
              近期未收到运行检查，请核对监测节点与通信状态。
            </p>
          )}
          {incident.data.acknowledgedBy ? (
            <>
              <small>
                {incident.data.acknowledgedName || incident.data.acknowledgedBy}{" "}
                已接手 · {date(incident.data.acknowledgedAt)}
              </small>
              <p className="runtime-incident-note">
                {incident.data.acknowledgementNote}
              </p>
            </>
          ) : (
            <small>{recovered ? "恢复前无人接手" : "尚未接手"}</small>
          )}
          {recovered && (
            <small>
              {date(incident.data.resolvedAt)} ·{" "}
              {signal ? "已重新收到可信运行检查" : "连续两次健康检查通过"}
            </small>
          )}
        </div>
        {!recovered && !incident.data.acknowledgedBy && (
          <Button
            secondary
            disabled={w.actor.kind !== "human" || !w.can("release", "quality")}
            onClick={() => takeOwnership(incident)}
          >
            确认接手
          </Button>
        )}
      </article>
    );
  }

  return (
    <section className="runtime-incidents" aria-label="运行事件">
      <h4>运行事件{active.length > 0 ? `（${active.length}）` : ""}</h4>
      {!active.length && (
        <p className="form-note">当前没有未恢复的运行事件。</p>
      )}
      {active.map(row)}
      {resolved.length > 0 && (
        <details className="runtime-incident-history">
          <summary>已恢复事件（{resolved.length}）</summary>
          {resolved.map(row)}
        </details>
      )}
    </section>
  );
}
