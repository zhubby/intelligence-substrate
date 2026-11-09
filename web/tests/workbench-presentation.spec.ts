import { test, expect } from "@playwright/test";
import {
  buildTaskGraph,
  knownSeries,
  metricValue,
  type TaskScope,
} from "../src/presentation";
import type { Resource } from "../src/api";

const scope: TaskScope = {
  projectId: "project",
  goalId: "goal",
  planId: "plan",
  requirementRevision: 2,
};
const resource = (
  id: string,
  kind: string,
  data: Record<string, unknown>,
  overrides: Partial<Resource> = {},
): Resource => ({
  id,
  kind,
  projectId: "project",
  goalId: "goal",
  title: id,
  status: "ready",
  revision: 1,
  data,
  createdAt: "2026-11-08T00:00:00Z",
  updatedAt: "2026-11-08T00:00:00Z",
  ...overrides,
});
const plan = resource("plan", "plan", {
  requirementRevision: 2,
  tasks: [
    { id: "analysis", title: "分析", role: "analysis", runtime: "linux" },
    { id: "review", title: "复核", dependsOn: ["analysis"] },
  ],
});
const task = (id: string, logicalId: string, data = {}, overrides = {}) =>
  resource(
    id,
    "task",
    { logicalId, planId: "plan", requirementRevision: 2, ...data },
    overrides,
  );

test("dependency graph binds exact project, goal, plan and requirement revision", () => {
  const inputs = [
    task("current", "analysis"),
    task("review-task", "review", { dependsOn: ["analysis"] }),
    task("old-requirements", "analysis", { requirementRevision: 1 }),
    task("old-plan", "analysis", { planId: "old" }),
    task("other-project", "analysis", {}, { projectId: "foreign" }),
    task("other-goal", "analysis", {}, { goalId: "foreign" }),
  ];
  for (const mode of ["plan", "execution"] as const) {
    const graph = buildTaskGraph(scope, plan, inputs, mode);
    expect(graph.nodes.map((node) => node.taskId)).toEqual([
      "current",
      "review-task",
    ]);
    expect(graph.edges).toHaveLength(1);
    expect(graph.nodes[1].column).toBe(1);
    expect(graph.issues).toEqual([]);
  }
  expect(
    buildTaskGraph({ ...scope, requirementRevision: 1 }, plan, inputs, "plan")
      .nodes,
  ).toEqual([]);
  expect(
    buildTaskGraph({ ...scope, projectId: "other" }, plan, inputs, "execution")
      .nodes,
  ).toEqual([]);
});

test("proposed recipes never borrow historical work items and dynamic tasks stay scoped", () => {
  const inputs = [
    task("old", "analysis", { planId: "previous" }),
    task("repair", "repair", { dependsOn: ["analysis"] }),
  ];
  const proposed = buildTaskGraph(scope, plan, inputs, "plan");
  expect(proposed.nodes[0].taskId).toBeUndefined();
  expect(proposed.nodes.map((node) => node.id)).toEqual(["analysis", "review"]);
  const execution = buildTaskGraph(scope, plan, inputs, "execution");
  expect(execution.nodes.map((node) => node.id)).toEqual(["repair"]);
  expect(execution.issues.join(" ")).toContain("不在此版本中");
});

test("cycles, duplicate bindings and missing dependencies are visible without fabricated nodes", () => {
  const invalid = resource("plan", "plan", {
    requirementRevision: 2,
    tasks: [
      { id: "analysis", dependsOn: ["review", "missing"] },
      { id: "review", dependsOn: ["analysis"] },
      { id: "review" },
    ],
  });
  const graph = buildTaskGraph(
    scope,
    invalid,
    [task("first", "analysis"), task("duplicate", "analysis")],
    "plan",
  );
  expect(graph.nodes).toHaveLength(2);
  expect(graph.nodes[0].taskId).toBeUndefined();
  expect(graph.edges).toHaveLength(2);
  expect(graph.issues.join(" ")).toMatch(/重复/);
  expect(graph.issues.join(" ")).toMatch(/多个工作项/);
  expect(graph.issues.join(" ")).toMatch(/循环/);
  expect(graph.issues.join(" ")).toMatch(/不在此版本中/);
});

test("unknown and invalid metrics remain absent while confirmed zero remains zero", () => {
  for (const value of [null, undefined, NaN, Infinity, -1, "0"]) {
    expect(metricValue(value).value).toBeNull();
    expect(metricValue(value).text).toContain("待采集");
  }
  expect(metricValue(0)).toEqual({ value: 0, text: "0" });
  expect(
    knownSeries(
      { unknown: null, zero: 0, positive: 3 },
      { unknown: "未知", zero: "零", positive: "有值" },
    ),
  ).toEqual([
    { name: "零", value: 0 },
    { name: "有值", value: 3 },
  ]);
});
