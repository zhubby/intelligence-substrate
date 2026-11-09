import type { ReactNode } from "react";
import { Tabs } from "@mantine/core";

export function WorkbenchTabs({
  label,
  value,
  onChange,
  items,
  indicators = [],
  className = "",
  children,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  items: string[][];
  indicators?: string[];
  className?: string;
  children: ReactNode;
}) {
  return (
    <Tabs
      value={value}
      onChange={(next) => {
        if (next) onChange(next);
      }}
      keepMounted={false}
      className="workbench-tabs"
    >
      <Tabs.List aria-label={label} className={"tabs " + className}>
        {items.map(([key, title]) => (
          <Tabs.Tab
            key={key}
            value={key}
            rightSection={
              indicators.includes(key) ? (
                <span className="tab-dot" />
              ) : undefined
            }
          >
            {title}
          </Tabs.Tab>
        ))}
      </Tabs.List>
      <Tabs.Panel value={value}>{children}</Tabs.Panel>
    </Tabs>
  );
}
