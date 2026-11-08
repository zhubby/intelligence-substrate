import {
  useId,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import {
  ArrowUpRight,
  Check,
  ChevronRight,
  LoaderCircle,
  Plus,
  X,
} from "lucide-react";
import { states, type Data } from "./api";
import {
  LooseleafBadge,
  LooseleafButton,
  LooseleafDialog,
  LooseleafPaper,
} from "./components/ui/looseleaf";
import { cn } from "./components/ui/cn";

const statusTones: Record<
  string,
  "green" | "red" | "blue" | "yellow" | "neutral"
> = {
  active: "green",
  online: "green",
  healthy: "green",
  passed: "green",
  completed: "green",
  committed: "green",
  saved: "green",
  ok: "green",
  approved: "green",
  verified: "green",
  delivered: "green",
  succeeded: "green",
  merged: "green",
  resolved: "green",
  recorded: "green",
  configured: "green",
  settled: "green",
  answered: "green",
  confirmed: "green",
  recovered: "green",
  failed: "red",
  rejected: "red",
  unhealthy: "red",
  blocked: "red",
  cancelled: "red",
  abandoned: "red",
  unresolved: "red",
  halted: "red",
  running: "blue",
  developing: "blue",
  prepared: "blue",
  scheduled: "blue",
  dispatched: "blue",
  publishing: "blue",
  submitting: "blue",
  published: "blue",
  repairing: "blue",
  leased: "blue",
  awaiting_baseline: "yellow",
  planning: "yellow",
  proposed: "yellow",
  open: "yellow",
  triaged: "yellow",
  pending: "yellow",
  awaiting_approval: "yellow",
  processing: "yellow",
  awaiting_review: "yellow",
  awaiting_release: "yellow",
  awaiting_signature: "yellow",
  waiting_signature: "yellow",
  reconciliation_required: "yellow",
  revising_materials: "yellow",
  ready: "yellow",
  unknown: "yellow",
  uncertain: "yellow",
  waiting: "yellow",
  waiting_message: "yellow",
  suspended: "yellow",
  awaiting_verification: "yellow",
  reserved: "yellow",
  worker_required: "yellow",
  credentials_required: "yellow",
  local_only: "yellow",
  closed: "neutral",
  disabled: "neutral",
  draft: "neutral",
  expired: "neutral",
  offline: "neutral",
  revoked: "neutral",
  stale: "neutral",
  stopped: "neutral",
  superseded: "neutral",
  unconfigured: "neutral",
  voided: "neutral",
};

export function Badge({ status }: { status: string }) {
  const tone = statusTones[status] || "neutral";
  return (
    <LooseleafBadge
      tone={tone}
      className={cn("badge status-" + status, "status-sticker")}
    >
      <span />
      {states[status] || status}
    </LooseleafBadge>
  );
}
export function Empty({
  title,
  description,
  action,
  icon,
}: {
  title: string;
  description: string;
  action?: ReactNode;
  icon?: ReactNode;
}) {
  return (
    <LooseleafPaper className="empty-state">
      <div className="empty-symbol">{icon || <Plus size={26} />}</div>
      <h3>{title}</h3>
      <p>{description}</p>
      {action}
    </LooseleafPaper>
  );
}
export function Button({
  children,
  secondary = false,
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement> & { secondary?: boolean }) {
  return (
    <LooseleafButton
      {...props}
      variant={secondary ? "outline" : "default"}
      className={cn(secondary ? "button secondary" : "button", props.className)}
    >
      {children}
    </LooseleafButton>
  );
}
export function SectionHeading({
  eyebrow,
  title,
  description,
  action,
}: {
  eyebrow?: string;
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <div className="section-heading">
      <div>
        {eyebrow && <span className="eyebrow">{eyebrow}</span>}
        <h2>{title}</h2>
        {description && <p>{description}</p>}
      </div>
      {action}
    </div>
  );
}
export function Modal({
  title,
  children,
  onClose,
}: {
  title: string;
  children: ReactNode;
  onClose: () => void;
}) {
  return (
    <LooseleafDialog.Root
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <LooseleafDialog.Portal>
        <LooseleafDialog.Overlay className="modal-overlay ll-dialog-overlay" />
        <LooseleafDialog.Content
          className="modal ll-dialog-content"
          aria-describedby={undefined}
        >
          <header>
            <LooseleafDialog.Title>{title}</LooseleafDialog.Title>
            <LooseleafDialog.Close asChild>
              <button
                className="icon-button"
                aria-label="关闭对话框"
                type="button"
              >
                <X size={20} />
              </button>
            </LooseleafDialog.Close>
          </header>
          {children}
        </LooseleafDialog.Content>
      </LooseleafDialog.Portal>
    </LooseleafDialog.Root>
  );
}
export interface Field {
  name: string;
  label: string;
  type?:
    | "textarea"
    | "select"
    | "number"
    | "password"
    | "checkbox"
    | "file"
    | "datetime-local";
  required?: boolean;
  options?: Record<string, string>;
  placeholder?: string;
  hint?: string;
  defaultValue?: any;
  step?: string;
}
export function Form({
  fields,
  onSubmit,
  submit = "保存",
  children,
}: {
  fields: Field[];
  onSubmit: (data: Data) => Promise<void>;
  submit?: string;
  children?: ReactNode;
}) {
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const formID = useId();
  async function save(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    const values: Data = {};
    for (const f of fields) {
      const v = form.get(f.name);
      values[f.name] =
        f.type === "checkbox"
          ? v === "on"
          : f.type === "number"
            ? Number(v)
            : v;
    }
    setPending(true);
    setError("");
    try {
      await onSubmit(values);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setPending(false);
    }
  }
  return (
    <form className="form ll-form" onSubmit={save}>
      {fields.map((f) => (
        <label
          className={f.type === "checkbox" ? "checkbox-field" : "field"}
          key={f.name}
        >
          <span>
            {f.type === "checkbox" && (
              <input
                type="checkbox"
                name={f.name}
                aria-label={f.label}
                aria-describedby={f.hint ? formID + f.name : undefined}
                defaultChecked={f.defaultValue}
              />
            )}{" "}
            {f.label}
          </span>
          {f.type === "textarea" ? (
            <textarea
              name={f.name}
              aria-label={f.label}
              aria-describedby={f.hint ? formID + f.name : undefined}
              required={f.required}
              placeholder={f.placeholder}
              defaultValue={f.defaultValue}
              rows={5}
            />
          ) : f.type === "select" ? (
            <select
              name={f.name}
              aria-label={f.label}
              aria-describedby={f.hint ? formID + f.name : undefined}
              defaultValue={f.defaultValue}
              required={f.required}
            >
              {!f.defaultValue && <option value="">请选择</option>}
              {Object.entries(f.options || {}).map(([value, label]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </select>
          ) : f.type !== "checkbox" ? (
            <input
              type={f.type || "text"}
              name={f.name}
              aria-label={f.label}
              aria-describedby={f.hint ? formID + f.name : undefined}
              required={f.required}
              placeholder={f.placeholder}
              defaultValue={f.defaultValue}
              step={f.step ?? (f.type === "number" ? "0.01" : undefined)}
            />
          ) : null}
          {f.hint && <small id={formID + f.name}>{f.hint}</small>}
        </label>
      ))}
      {children}
      {error && (
        <div className="form-error" role="alert">
          {error}
        </div>
      )}
      <footer>
        <Button type="submit" disabled={pending}>
          {pending ? (
            <LoaderCircle className="spin" size={16} />
          ) : (
            <Check size={16} />
          )}{" "}
          {pending ? "正在保存…" : submit}
        </Button>
      </footer>
    </form>
  );
}
export function Confirm({
  description,
  onConfirm,
  label = "确认批准",
}: {
  description: ReactNode;
  onConfirm: () => Promise<void>;
  label?: string;
}) {
  return (
    <Form fields={[]} onSubmit={onConfirm} submit={label}>
      <div className="confirmation-copy">{description}</div>
    </Form>
  );
}
export function ExternalLink({
  href,
  children,
}: {
  href: string;
  children: ReactNode;
}) {
  return (
    <a href={href} target="_blank" rel="noreferrer">
      {children}
      <ArrowUpRight size={13} />
    </a>
  );
}
export function Breadcrumb({ children }: { children: ReactNode }) {
  return (
    <div className="breadcrumb">
      <span>工作台</span>
      <ChevronRight size={14} />
      {children}
    </div>
  );
}
