import { lazy, Suspense, type ReactNode } from "react";
import type { Resource } from "./api";

export type SpecialistProps =
  | { kind: "message"; header?: ReactNode; children: ReactNode }
  | { kind: "steps"; steps: Resource[] };

const SpecialistContent = lazy(() => import("./SpecialistContent"));

export function MessageBubble({
  children,
  header,
}: {
  children: ReactNode;
  header?: ReactNode;
}) {
  return (
    <Suspense
      fallback={
        <div>
          {header}
          {children}
        </div>
      }
    >
      <SpecialistContent kind="message" header={header}>
        {children}
      </SpecialistContent>
    </Suspense>
  );
}

export function RecordedSteps({ steps }: { steps: Resource[] }) {
  if (!steps.length) return null;
  return (
    <section
      className="recorded-steps source-details"
      aria-label="已记录执行步骤"
    >
      <h4>已记录执行步骤 · {steps.length}</h4>
      <p className="muted">
        仅展示平台保存的工具操作与结果，不是模型内部思维；未确认操作不表示已完成。
      </p>
      <Suspense fallback={<p role="status">正在载入操作记录…</p>}>
        <SpecialistContent kind="steps" steps={steps} />
      </Suspense>
    </section>
  );
}
