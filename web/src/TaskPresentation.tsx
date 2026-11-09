import { lazy, Suspense, useState, type ReactNode } from "react";
import { Group, Radio, VisuallyHidden } from "@mantine/core";
import type { Resource } from "./api";
import { buildTaskGraph, type TaskScope } from "./presentation";

const TaskGraph = lazy(() => import("./TaskGraph"));

export function TaskPresentation({
  scope,
  plan,
  resources,
  mode,
  label,
  graphLabel,
  onOpenTask,
  children,
}: {
  scope: TaskScope;
  plan: Resource;
  resources: Resource[];
  mode: "plan" | "execution";
  label: string;
  graphLabel: string;
  onOpenTask: (taskId: string) => void;
  children: ReactNode;
}) {
  const [view, setView] = useState("list");
  return (
    <div className="task-presentation">
      <div className="task-view-toolbar">
        <small className="muted">
          需求 v{scope.requirementRevision} ·{" "}
          {mode === "plan" ? "批准方案的任务依赖" : "当前方案的真实工作项"}
        </small>
        <Radio.Group
          label={<VisuallyHidden>{label}</VisuallyHidden>}
          value={view}
          onChange={setView}
          className="task-view-options"
        >
          <Group gap="md">
            <Radio value="list" label="列表" size="xs" />
            <Radio value="graph" label="依赖图" size="xs" />
          </Group>
        </Radio.Group>
      </div>
      {view === "list" ? (
        children
      ) : (
        <Suspense fallback={<p role="status">正在载入只读依赖图…</p>}>
          <TaskGraph
            model={buildTaskGraph(scope, plan, resources, mode)}
            label={graphLabel}
            onOpenTask={onOpenTask}
          />
        </Suspense>
      )}
    </div>
  );
}
