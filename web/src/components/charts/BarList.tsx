import { forwardRef, useMemo, type HTMLAttributes } from "react";
import { cn } from "../ui/cn";

export interface BarDatum {
  name: string;
  value: number;
  key?: string;
}
interface BarListProps extends HTMLAttributes<HTMLDivElement> {
  data: BarDatum[];
  valueFormatter?: (value: number) => string;
  sortOrder?: "ascending" | "descending" | "none";
}

export const BarList = forwardRef<HTMLDivElement, BarListProps>(
  function BarList(
    {
      data = [],
      valueFormatter = String,
      sortOrder = "descending",
      className,
      ...props
    },
    forwardedRef,
  ) {
    const sortedData = useMemo(
      () =>
        sortOrder === "none"
          ? data
          : [...data].sort((first, second) =>
              sortOrder === "ascending"
                ? first.value - second.value
                : second.value - first.value,
            ),
      [data, sortOrder],
    );
    const widths = useMemo(() => {
      const maximum = Math.max(...sortedData.map((item) => item.value), 0);
      return sortedData.map((item) =>
        item.value === 0 ? 0 : Math.max((item.value / maximum) * 100, 2),
      );
    }, [sortedData]);
    return (
      <div
        ref={forwardedRef}
        className={cn("tremor-bar-list", className)}
        tremor-id="tremor-raw"
        {...props}
      >
        <div className="tremor-bars">
          {sortedData.map((item, index) => (
            <div key={item.key ?? item.name} className="tremor-bar-row">
              <div
                className="tremor-bar-fill"
                style={{ width: `${widths[index]}%` }}
              />
              <span>{item.name}</span>
            </div>
          ))}
        </div>
        <div className="tremor-bar-values">
          {sortedData.map((item) => (
            <div key={item.key ?? item.name}>{valueFormatter(item.value)}</div>
          ))}
        </div>
      </div>
    );
  },
);
