import { lazy, Suspense } from "react";
import type { BarDatum } from "./components/charts/BarList";
import { metricValue } from "./presentation";

const Charts = lazy(() => import("./Charts"));

export function ChartPanel({
  title,
  description,
  data,
  format = "count",
  variant = "bar",
}: {
  title: string;
  description: string;
  data: BarDatum[];
  format?: "count" | "money";
  variant?: "bar" | "donut";
}) {
  const known = data.filter((item) => metricValue(item.value).value !== null);
  return (
    <section className="chart-panel" aria-label={title}>
      <div className="content-heading">
        <h3>{title}</h3>
        <span className="eyebrow">RECORDED DATA</span>
      </div>
      <p className="form-note">{description}</p>
      {known.length ? (
        <Suspense fallback={<p role="status">正在载入数据展示…</p>}>
          <Charts data={known} variant={variant} format={format} />
        </Suspense>
      ) : (
        <p className="inline-empty">待采集 / 待核对 · 暂无可核算数据</p>
      )}
    </section>
  );
}
