import type { Resource } from "./api";

export interface TaskScope {
  projectId: string;
  goalId: string;
  planId: string;
  requirementRevision: number;
}

export type GraphTask = {
  id: string;
  title: string;
  role: string;
  runtime: string;
  status: string;
  taskId?: string;
  dependencies: string[];
  column: number;
  row: number;
};

export interface TaskGraphModel {
  nodes: GraphTask[];
  edges: { id: string; source: string; target: string }[];
  issues: string[];
}

export function buildTaskGraph(
  scope: TaskScope,
  plan: Resource,
  resources: Resource[],
  mode: "plan" | "execution",
): TaskGraphModel {
  const issues: string[] = [];
  if (
    plan.kind !== "plan" ||
    plan.id !== scope.planId ||
    plan.projectId !== scope.projectId ||
    plan.goalId !== scope.goalId ||
    !Number.isInteger(scope.requirementRevision) ||
    scope.requirementRevision < 1 ||
    plan.data.requirementRevision !== scope.requirementRevision
  )
    return {
      nodes: [],
      edges: [],
      issues: ["方案与项目、目标或需求版本不一致，未展示依赖图。"],
    };

  const tasks = resources.filter(
    (task) =>
      task.kind === "task" &&
      task.projectId === scope.projectId &&
      task.goalId === scope.goalId &&
      task.data.planId === scope.planId &&
      task.data.requirementRevision === scope.requirementRevision,
  );
  const matches = new Map<string, Resource[]>();
  for (const task of tasks) {
    const logicalId = task.data.logicalId;
    if (typeof logicalId !== "string" || !logicalId) {
      issues.push(`工作项「${task.title}」缺少逻辑 ID，未加入图。`);
      continue;
    }
    matches.set(logicalId, [...(matches.get(logicalId) || []), task]);
  }
  const recipes = Array.isArray(plan.data.tasks) ? plan.data.tasks : [];
  const inputs =
    mode === "plan"
      ? recipes.map((recipe) => ({ ...recipe, logicalId: recipe.id }))
      : tasks.map((task) => ({ ...task.data, title: task.title }));
  const nodes: GraphTask[] = [];
  const seen = new Set<string>();
  for (const input of inputs) {
    const logicalId = input.logicalId;
    if (typeof logicalId !== "string" || !logicalId) {
      issues.push("有工作项缺少逻辑 ID，未加入图。");
      continue;
    }
    if (seen.has(logicalId)) {
      issues.push(`逻辑 ID「${logicalId}」重复，请核对方案。`);
      continue;
    }
    seen.add(logicalId);
    const bound = matches.get(logicalId) || [];
    if (bound.length > 1)
      issues.push(`逻辑 ID「${logicalId}」绑定了多个工作项，详情暂不可用。`);
    const task = bound.length === 1 ? bound[0] : undefined;
    nodes.push({
      id: logicalId,
      title: typeof input.title === "string" ? input.title : logicalId,
      role: typeof input.role === "string" ? input.role : "",
      runtime: typeof input.runtime === "string" ? input.runtime : "",
      status: task?.status || "draft",
      taskId: task?.id,
      dependencies: Array.isArray(input.dependsOn)
        ? [
            ...new Set<string>(
              (input.dependsOn as unknown[]).filter(
                (value): value is string =>
                  typeof value === "string" && Boolean(value),
              ),
            ),
          ]
        : [],
      column: 0,
      row: 0,
    });
  }
  const byId = new Map(nodes.map((node) => [node.id, node]));
  const edges: TaskGraphModel["edges"] = [];
  for (const node of nodes) {
    for (const dependency of node.dependencies) {
      if (!byId.has(dependency)) {
        issues.push(`「${node.title}」的依赖「${dependency}」不在此版本中。`);
        continue;
      }
      edges.push({
        id: JSON.stringify([dependency, node.id]),
        source: dependency,
        target: node.id,
      });
    }
  }
  const remaining = new Map(
    nodes.map((node) => [
      node.id,
      edges.filter((edge) => edge.target === node.id).length,
    ]),
  );
  const queue = nodes.filter((node) => remaining.get(node.id) === 0);
  const visited = new Set<string>();
  for (let index = 0; index < queue.length; index += 1) {
    const node = queue[index];
    visited.add(node.id);
    for (const edge of edges.filter(
      (candidate) => candidate.source === node.id,
    )) {
      const target = byId.get(edge.target)!;
      target.column = Math.max(target.column, node.column + 1);
      const count = remaining.get(target.id)! - 1;
      remaining.set(target.id, count);
      if (count === 0) queue.push(target);
    }
  }
  if (visited.size !== nodes.length)
    issues.push(
      "依赖包含循环或受循环阻塞的工作项；请核对方案，图不会修改依赖。",
    );
  const rows = new Map<number, number>();
  for (const node of nodes) {
    node.row = rows.get(node.column) || 0;
    rows.set(node.column, node.row + 1);
  }
  return { nodes, edges, issues: [...new Set(issues)] };
}

export interface MetricValue {
  value: number | null;
  text: string;
}

export function metricValue(
  value: unknown,
  format: (value: number) => string = String,
): MetricValue {
  return typeof value === "number" && Number.isFinite(value) && value >= 0
    ? { value, text: format(value) }
    : { value: null, text: "待采集 / 待核对" };
}

export function knownSeries(
  values: Record<string, unknown>,
  labels: Record<string, string>,
) {
  return Object.entries(labels).flatMap(([key, name]) => {
    const metric = metricValue(values[key]);
    return metric.value === null ? [] : [{ name, value: metric.value }];
  });
}
