import { money } from "./api";
import { BarList, type BarDatum } from "./components/charts/BarList";
import { DonutChart } from "./components/charts/DonutChart";

export default function Charts({
  data,
  variant,
  format,
}: {
  data: BarDatum[];
  variant: "bar" | "donut";
  format: "count" | "money";
}) {
  const formatter =
    format === "money" ? money : (value: number) => String(value);
  return variant === "bar" ? (
    <BarList data={data} valueFormatter={formatter} />
  ) : (
    <div className="donut-layout">
      <div aria-hidden="true">
        <DonutChart data={data} valueFormatter={formatter} />
        {data.every((item) => item.value === 0) && (
          <small>已记录数值均为零</small>
        )}
      </div>
      <dl className="chart-legend">
        {data.map((item) => (
          <div key={item.name}>
            <dt>{item.name}</dt>
            <dd>{formatter(item.value)}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}
