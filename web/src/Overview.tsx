import type { ReactNode } from "react";
import { Paper } from "@mantine/core";
import {
  Activity,
  CircleDollarSign,
  FileCheck2,
  GitBranch,
  Layers3,
  ShieldCheck,
  Target,
  UserRoundCheck,
} from "lucide-react";
import { money, type Resource } from "./api";
import { metricValue } from "./presentation";

export function OverviewGrid({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <section className="overview-grid" aria-label={label}>
      {children}
    </section>
  );
}

export function StatCard({
  label,
  value,
  note,
  icon,
  tone = "neutral",
}: {
  label: string;
  value: ReactNode;
  note: string;
  icon: ReactNode;
  tone?: "neutral" | "accent" | "warning" | "success";
}) {
  return (
    <Paper withBorder className={"stat-card stat-" + tone}>
      <div className="stat-label">
        <span>{label}</span>
        <span className="stat-icon" aria-hidden="true">
          {icon}
        </span>
      </div>
      <strong className="stat-value">{value}</strong>
      <small>{note}</small>
    </Paper>
  );
}

export function DeliveryOverview({
  goals,
  tasks,
  pending,
}: {
  goals: Resource[];
  tasks: Resource[];
  pending: number;
}) {
  const amounts = goals.map((goal) => metricValue(goal.data.spentCents));
  const total =
    amounts.length > 0 && amounts.every((amount) => amount.value !== null)
      ? money(amounts.reduce((sum, amount) => sum + amount.value!, 0))
      : "待核对";
  return (
    <OverviewGrid label="交付概览">
      <StatCard
        label="交付目标"
        value={goals.length}
        note={
          goals.filter((goal) => goal.status === "delivered").length +
          " 个已交付 · 当前项目"
        }
        icon={<Target size={17} />}
      />
      <StatCard
        label="执行中工作项"
        value={tasks.filter((task) => task.status === "running").length}
        note={tasks.length + " 个工作项 · 当前目标与基线"}
        icon={<Activity size={17} />}
        tone="accent"
      />
      <StatCard
        label="待人工决策"
        value={pending}
        note="需求基线、方案与发布仍需人工批准"
        icon={<UserRoundCheck size={17} />}
        tone={pending > 0 ? "warning" : "neutral"}
      />
      <StatCard
        label="模型费用"
        value={total}
        note="项目目标已记费用 · 完整成本见企业治理"
        icon={<CircleDollarSign size={17} />}
      />
    </OverviewGrid>
  );
}

export function CodeOverview({
  repositories,
  reviews,
}: {
  repositories: Resource[];
  reviews: Resource[];
}) {
  return (
    <OverviewGrid label="代码与评审概览">
      <StatCard
        label="代码仓库"
        value={repositories.length}
        note="真实 Git 仓库 · 当前项目"
        icon={<GitBranch size={17} />}
      />
      <StatCard
        label="评审记录"
        value={reviews.length}
        note="独立评审与 CI 分别记录"
        icon={<FileCheck2 size={17} />}
        tone="accent"
      />
      <StatCard
        label="待合并评审"
        value={
          reviews.filter(
            (review) =>
              !["merged", "rejected", "closed"].includes(review.status),
          ).length
        }
        note="合并仍须满足现有保护规则"
        icon={<Layers3 size={17} />}
      />
    </OverviewGrid>
  );
}

export function QualityOverview({
  tasks,
  evidence,
  defects,
}: {
  tasks: Resource[];
  evidence: Resource[];
  defects: Resource[];
}) {
  return (
    <OverviewGrid label="分层质量状态">
      <StatCard
        label="Agent 完成声明"
        value={tasks.filter((task) => task.status === "succeeded").length}
        note="工作项完成不等于独立验证通过"
        icon={<Activity size={17} />}
      />
      <StatCard
        label="CI 通过记录"
        value={
          evidence.filter(
            (record) =>
              record.status === "passed" &&
              record.data.verificationEnvironment === "ci",
          ).length
        }
        note="仅代表所记录 CI 环境中的验证"
        icon={<FileCheck2 size={17} />}
        tone="accent"
      />
      <StatCard
        label="目标环境通过记录"
        value={
          evidence.filter(
            (record) =>
              record.status === "passed" &&
              [
                "browser",
                "windows",
                "macos",
                "ios-device",
                "android-device",
              ].includes(record.data.verificationEnvironment),
          ).length
        }
        note="按证据区分浏览器、系统与实际设备"
        icon={<ShieldCheck size={17} />}
        tone="success"
      />
      <StatCard
        label="未关闭缺陷"
        value={
          defects.filter(
            (defect) => !["closed", "resolved"].includes(defect.status),
          ).length
        }
        note="保留失败、修复与复测来源"
        icon={<Layers3 size={17} />}
        tone="warning"
      />
    </OverviewGrid>
  );
}
