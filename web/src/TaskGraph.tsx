import { useState, type CSSProperties } from "react";
import { useComputedColorScheme } from "@mantine/core";
import {
  Background,
  Controls,
  Handle,
  Position,
  ReactFlow,
  type Node,
  type NodeProps,
} from "@xyflow/react";
import { roles } from "./api";
import { Badge, Empty } from "./ui";
import type { GraphTask, TaskGraphModel } from "./presentation";
import "@xyflow/react/dist/style.css";

type TaskNode = Node<
  GraphTask & { onOpenTask: (taskId: string) => void },
  "task"
>;

function TaskNodeContent({ data }: NodeProps<TaskNode>) {
  return (
    <div className="dependency-node">
      <Handle type="target" position={Position.Left} isConnectable={false} />
      <span className="dependency-logical-id">{data.id}</span>
      {data.taskId ? (
        <button
          className="nodrag work-title"
          type="button"
          aria-label={"查看工作项：" + data.title}
          onClick={() => data.onOpenTask(data.taskId!)}
        >
          <strong>{data.title}</strong>
        </button>
      ) : (
        <strong>{data.title}</strong>
      )}
      <small>
        {roles[data.role] || data.role} · {data.runtime}
      </small>
      {data.taskId ? (
        <Badge status={data.status} />
      ) : (
        <small>尚未创建执行工作项</small>
      )}
      <Handle type="source" position={Position.Right} isConnectable={false} />
    </div>
  );
}

const nodeTypes = { task: TaskNodeContent };

export default function TaskGraph({
  model,
  label,
  onOpenTask,
}: {
  model: TaskGraphModel;
  label: string;
  onOpenTask: (taskId: string) => void;
}) {
  const scheme = useComputedColorScheme("dark");
  const [selectedId, setSelectedId] = useState<string>();
  const nodes: TaskNode[] = model.nodes.map((task) => ({
    id: task.id,
    type: "task",
    position: { x: task.column * 330, y: task.row * 190 },
    data: { ...task, onOpenTask },
    selected: selectedId === task.id,
    style: { width: 260 } as CSSProperties,
  }));
  return (
    <section className="dependency-view" role="region" aria-label={label}>
      <p className="form-note">
        只读 ·
        可选择、缩放和平移。打开工作项查看现有详情，不会修改依赖或触发执行。
      </p>
      {model.issues.length > 0 && (
        <div className="risk-note" role="status">
          {model.issues.map((issue) => (
            <p key={issue}>{issue}</p>
          ))}
        </div>
      )}
      {nodes.length === 0 ? (
        <Empty
          title="此版本暂无可展示的依赖"
          description="仅展示与当前项目、目标、方案和需求版本一致的工作项。"
        />
      ) : (
        <div className="dependency-canvas">
          <ReactFlow<TaskNode>
            nodes={nodes}
            edges={model.edges}
            nodeTypes={nodeTypes}
            onNodeClick={(_, node) => setSelectedId(node.id)}
            onPaneClick={() => setSelectedId(undefined)}
            onNodesChange={(changes) => {
              const selection = changes.find(
                (change) => change.type === "select" && change.selected,
              );
              if (selection && "id" in selection) setSelectedId(selection.id);
            }}
            nodesDraggable={false}
            nodesConnectable={false}
            edgesReconnectable={false}
            edgesFocusable={false}
            deleteKeyCode={null}
            connectOnClick={false}
            colorMode={scheme}
            minZoom={0.3}
            maxZoom={1.5}
            fitView
            fitViewOptions={{ padding: 0.2, maxZoom: 1 }}
            ariaLabelConfig={{
              "node.a11yDescription.default":
                "只读工作项。可选择节点，或使用内部按钮查看工作项详情。不能修改或删除依赖。",
              "node.a11yDescription.keyboardDisabled":
                "只读工作项。使用 Tab 移动到查看工作项按钮并按 Enter 打开详情。",
              "controls.ariaLabel": "依赖图视图控制",
              "controls.zoomIn.ariaLabel": "放大依赖图",
              "controls.zoomOut.ariaLabel": "缩小依赖图",
              "controls.fitView.ariaLabel": "适应依赖图",
              "handle.ariaLabel": "只读依赖端点",
            }}
          >
            <Background gap={20} size={1} />
            <Controls showInteractive={false} />
          </ReactFlow>
        </div>
      )}
    </section>
  );
}
