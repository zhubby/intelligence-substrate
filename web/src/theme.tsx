import { useEffect, type ReactNode } from "react";
import {
  ActionIcon,
  MantineProvider,
  createTheme,
  localStorageColorSchemeManager,
  useComputedColorScheme,
  useMantineColorScheme,
  type CSSVariablesResolver,
} from "@mantine/core";
import { Moon, Sun } from "lucide-react";

export const colorSchemeKey = "is-workbench-color-scheme";

const theme = createTheme({
  primaryColor: "cyan",
  primaryShade: { dark: 4, light: 7 },
  fontFamily: "var(--font-body)",
  fontFamilyMonospace: "var(--font-code)",
  fontSizes: {
    xs: "0.75rem",
    sm: "0.8125rem",
    md: "0.875rem",
    lg: "1rem",
    xl: "1.125rem",
  },
  defaultRadius: "md",
  spacing: { xs: "8px", sm: "12px", md: "16px", lg: "24px", xl: "32px" },
  radius: { xs: "4px", sm: "6px", md: "8px", lg: "12px", xl: "16px" },
  respectReducedMotion: true,
  cursorType: "pointer",
  colors: {
    cyan: [
      "#e0faff",
      "#b6f0fa",
      "#80e4f3",
      "#4cd7ea",
      "#24c7df",
      "#0db3cc",
      "#0096b1",
      "#087e95",
      "#12667a",
      "#155467",
    ],
    dark: [
      "#dce4ef",
      "#a9b5c6",
      "#8693a7",
      "#526078",
      "#323f53",
      "#263246",
      "#1c2738",
      "#141d2b",
      "#101722",
      "#0c1017",
    ],
  },
});

const variables: CSSVariablesResolver = () => {
  const shared = {
    "--mantine-color-body": "var(--page)",
    "--mantine-color-text": "var(--ink)",
    "--mantine-color-dimmed": "var(--muted)",
    "--mantine-color-placeholder": "var(--muted)",
    "--mantine-color-default": "var(--surface)",
    "--mantine-color-default-hover": "var(--surface-hover)",
    "--mantine-color-default-color": "var(--ink)",
    "--mantine-color-default-border": "var(--line)",
    "--mantine-spacing-xs": "var(--space-2)",
    "--mantine-spacing-sm": "var(--space-3)",
    "--mantine-spacing-md": "var(--space-4)",
    "--mantine-spacing-lg": "var(--space-6)",
    "--mantine-spacing-xl": "var(--space-8)",
    "--mantine-radius-sm": "var(--radius-sm)",
    "--mantine-radius-md": "var(--radius-md)",
    "--mantine-radius-lg": "var(--radius-lg)",
  };
  return { variables: {}, light: shared, dark: shared };
};

function ThemeMetadata() {
  const scheme = useComputedColorScheme("dark");
  useEffect(() => {
    document.documentElement.style.colorScheme = scheme;
    document.documentElement.style.backgroundColor =
      scheme === "dark" ? "#0c1017" : "#f3f5f9";
    document
      .querySelector('meta[name="theme-color"]')
      ?.setAttribute("content", scheme === "dark" ? "#0c1017" : "#f3f5f9");
  }, [scheme]);
  return null;
}

export function WorkbenchProvider({ children }: { children: ReactNode }) {
  return (
    <MantineProvider
      theme={theme}
      cssVariablesResolver={variables}
      defaultColorScheme="dark"
      colorSchemeManager={localStorageColorSchemeManager({
        key: colorSchemeKey,
      })}
    >
      <ThemeMetadata />
      {children}
    </MantineProvider>
  );
}

export function ThemeToggle() {
  const scheme = useComputedColorScheme("dark");
  const { setColorScheme } = useMantineColorScheme();
  return (
    <ActionIcon
      className="theme-toggle"
      variant="default"
      size="lg"
      aria-label={scheme === "dark" ? "切换为浅色主题" : "切换为深色主题"}
      title={scheme === "dark" ? "切换为浅色主题" : "切换为深色主题"}
      onClick={() => setColorScheme(scheme === "dark" ? "light" : "dark")}
    >
      {scheme === "dark" ? <Sun size={17} /> : <Moon size={17} />}
    </ActionIcon>
  );
}
