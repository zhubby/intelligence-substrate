import { forwardRef, type HTMLAttributes } from "react";
import { Pie, PieChart, ResponsiveContainer, Tooltip } from "recharts";
import { cn } from "../ui/cn";
import type { BarDatum } from "./BarList";

const colors = [
  "var(--accent)",
  "var(--success)",
  "var(--warning)",
  "var(--info)",
  "var(--danger)",
  "var(--muted)",
];
interface DonutChartProps extends HTMLAttributes<HTMLDivElement> {
  data: BarDatum[];
  label?: string;
  valueFormatter?: (value: number) => string;
}

export const DonutChart = forwardRef<HTMLDivElement, DonutChartProps>(
  function DonutChart(
    { data = [], label, valueFormatter = String, className, ...props },
    forwardedRef,
  ) {
    const total = data.reduce((sum, item) => sum + item.value, 0);
    const parsedData = data.map((item, index) => ({
      ...item,
      fill: colors[index % colors.length],
    }));
    return (
      <div
        ref={forwardedRef}
        className={cn("tremor-donut", className)}
        tremor-id="tremor-raw"
        {...props}
      >
        <ResponsiveContainer width="100%" height="100%">
          <PieChart margin={{ top: 0, left: 0, right: 0, bottom: 0 }}>
            <text
              className="tremor-donut-label"
              x="50%"
              y="50%"
              textAnchor="middle"
              dominantBaseline="middle"
            >
              {label ?? valueFormatter(total)}
            </text>
            <Pie
              data={parsedData}
              cx="50%"
              cy="50%"
              startAngle={90}
              endAngle={-270}
              innerRadius="76%"
              outerRadius="96%"
              stroke="var(--surface)"
              strokeLinejoin="round"
              dataKey="value"
              nameKey="name"
              isAnimationActive={false}
            />
            <Tooltip
              isAnimationActive={false}
              content={({ active, payload }) =>
                active && payload?.length ? (
                  <div className="chart-tooltip">
                    {payload.map((item) => (
                      <div key={String(item.name)}>
                        <span>{item.name}</span>
                        <strong>{valueFormatter(Number(item.value))}</strong>
                      </div>
                    ))}
                  </div>
                ) : null
              }
            />
          </PieChart>
        </ResponsiveContainer>
      </div>
    );
  },
);
