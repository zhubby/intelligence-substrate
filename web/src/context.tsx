import { createContext, useContext, type ReactNode } from "react";
import {
  type Actor,
  type Resource,
  type Data,
  type Audit,
  type Integration,
} from "./api";
export type View =
  | "workspace"
  | "code"
  | "quality"
  | "releases"
  | "agents"
  | "governance";
export interface WorkspaceContext {
  actor: Actor;
  project: Resource;
  items: Resource[];
  audits: Audit[];
  integrations: Integration[];
  goal?: Resource;
  setGoal: (id: string) => void;
  select: (kind: string) => Resource[];
  refresh: () => Promise<void>;
  run: (name: string, data: Data) => Promise<Resource>;
  open: (title: string, node: ReactNode) => void;
  close: () => void;
  navigate: (view: View) => void;
  notify: (message: string) => void;
  can: (...roles: string[]) => boolean;
}
export const Workspace = createContext<WorkspaceContext | null>(null);
export function useWorkspace() {
  const value = useContext(Workspace);
  if (!value) throw new Error("Workspace unavailable");
  return value;
}
