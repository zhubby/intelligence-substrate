import { useState } from "react";
import { useComputedColorScheme } from "@mantine/core";
import { Bubble, ThoughtChain } from "@ant-design/x";
import { ConfigProvider, theme as antTheme } from "antd";
import { CheckCircle2, CircleHelp, CircleMinus } from "lucide-react";
import { date } from "./api";
import type { SpecialistProps } from "./Specialist";

export default function SpecialistContent(props: SpecialistProps) {
  const scheme = useComputedColorScheme("dark");
  const [expanded, setExpanded] = useState<string[]>([]);
  const computed = getComputedStyle(document.documentElement);
  const token = (name: string) => computed.getPropertyValue(name).trim();
  return (
    <ConfigProvider
      theme={{
        algorithm:
          scheme === "dark"
            ? antTheme.darkAlgorithm
            : antTheme.defaultAlgorithm,
        token: {
          colorPrimary: token("--accent"),
          colorSuccess: token("--success"),
          colorWarning: token("--warning"),
          colorError: token("--danger"),
          colorInfo: token("--info"),
          colorBgBase: token("--page"),
          colorBgContainer: token("--surface"),
          colorText: token("--ink"),
          colorTextSecondary: token("--muted"),
          colorBorder: token("--line"),
          fontSize: 14,
          borderRadius: 8,
          fontFamily: computed.fontFamily,
          motion: false,
        },
      }}
    >
      <div className="specialist-content">
        {props.kind === "message" ? (
          <Bubble
            content={props.children}
            header={props.header}
            variant="outlined"
            typing={false}
            streaming={false}
            styles={{
              content: {
                width: "100%",
                background: "var(--surface-subtle)",
                color: "var(--ink)",
                borderColor: "var(--line)",
              },
              body: { width: "100%", minWidth: 0 },
            }}
          />
        ) : (
          <ThoughtChain
            expandedKeys={expanded}
            onExpand={(keys) => setExpanded(keys.map(String))}
            items={props.steps.map((step) => ({
              key: step.id,
              icon:
                step.status === "completed" ? (
                  <CheckCircle2 size={17} />
                ) : step.status === "abandoned" ? (
                  <CircleMinus size={17} />
                ) : (
                  <CircleHelp size={17} />
                ),
              status:
                step.status === "completed"
                  ? "success"
                  : step.status === "abandoned"
                    ? "abort"
                    : undefined,
              blink: false,
              collapsible: true,
              title: (
                <button
                  type="button"
                  className="step-toggle"
                  aria-expanded={expanded.includes(step.id)}
                  aria-controls={"step-" + step.id}
                  onClick={(event) => {
                    event.stopPropagation();
                    setExpanded((current) =>
                      current.includes(step.id)
                        ? current.filter((key) => key !== step.id)
                        : [...current, step.id],
                    );
                  }}
                >
                  步骤 {step.data.step} ·{" "}
                  {step.data.action?.action || "工具操作"}
                </button>
              ),
              description: `${step.status === "completed" ? "结果已确认" : step.status === "abandoned" ? "已核对并放弃" : "结果待核对"} · ${date(step.createdAt)}`,
              content: (
                <div id={"step-" + step.id} className="recorded-step-content">
                  <dl>
                    <div>
                      <dt>执行记录</dt>
                      <dd>
                        <code>{step.data.runId}</code>
                      </dd>
                    </div>
                    <div>
                      <dt>操作摘要</dt>
                      <dd>
                        <code>{step.data.actionDigest}</code>
                      </dd>
                    </div>
                    <div>
                      <dt>需求基线</dt>
                      <dd>v{step.data.requirementRevision}</dd>
                    </div>
                  </dl>
                  {step.data.observation ? (
                    <pre className="code-block">{step.data.observation}</pre>
                  ) : (
                    <p className="form-note">
                      尚无已确认的工具观察。核对与恢复操作保留在原有进度入口。
                    </p>
                  )}
                </div>
              ),
            }))}
          />
        )}
      </div>
    </ConfigProvider>
  );
}
