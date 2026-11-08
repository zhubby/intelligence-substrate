import * as DialogPrimitive from "@radix-ui/react-dialog";
import { cva, type VariantProps } from "class-variance-authority";
import type { ComponentPropsWithoutRef, ReactNode } from "react";
import { cn } from "./cn";

const buttonVariants = cva(
  "ll-button relative isolate inline-flex min-h-9 shrink-0 items-center justify-center gap-2 whitespace-nowrap rounded-[3px] px-4 py-2 font-hand text-sm font-bold leading-none transition-[transform,filter,background-color] duration-150 ease-out focus-visible:outline-2 focus-visible:outline-offset-3 focus-visible:outline-ll-blue active:translate-y-px disabled:pointer-events-none disabled:opacity-45",
  {
    variants: {
      variant: {
        default: "bg-ll-marker text-ll-ink shadow-ll-ink hover:-rotate-[0.4deg] hover:bg-ll-marker-dark",
        outline: "border border-ll-line bg-ll-paper text-ll-ink shadow-ll-hair hover:-rotate-[0.4deg] hover:bg-ll-slip",
        secondary: "bg-ll-hi text-ll-ink shadow-ll-hair hover:-rotate-[0.4deg] hover:bg-ll-hi-deep",
        ghost: "bg-transparent text-ll-ink-soft hover:bg-ll-hi/40",
        danger: "bg-ll-red text-ll-paper shadow-ll-hair hover:-rotate-[0.4deg]",
      },
      size: {
        sm: "min-h-8 px-3 text-xs",
        md: "min-h-9 px-4 text-sm",
        lg: "min-h-10 px-5 text-base",
        icon: "size-9 p-0",
      },
    },
    defaultVariants: { variant: "default", size: "md" },
  },
);

export interface LooseleafButtonProps
  extends ComponentPropsWithoutRef<"button">,
    VariantProps<typeof buttonVariants> {}

export function LooseleafButton({
  className,
  variant,
  size,
  type = "button",
  ...props
}: LooseleafButtonProps) {
  return (
    <button
      type={type}
      {...props}
      className={cn(buttonVariants({ variant, size }), className)}
    />
  );
}

const badgeVariants = cva(
  "ll-badge inline-flex min-h-6 w-fit items-center gap-1.5 rounded-full border px-2.5 py-1 font-hand text-[11px] font-bold leading-none",
  {
    variants: {
      tone: {
        neutral: "border-ll-line bg-ll-paper text-ll-ink-soft",
        green: "border-ll-green/50 bg-ll-check text-ll-check-ink",
        yellow: "border-ll-hi-deep/50 bg-ll-hi text-ll-ink",
        orange: "border-ll-marker/50 bg-ll-sticky-orange text-ll-orange-deep",
        red: "border-ll-red/50 bg-ll-sticky-pink text-ll-red",
        blue: "border-ll-blue/50 bg-ll-sticky-blue text-ll-ink-you",
      },
    },
    defaultVariants: { tone: "neutral" },
  },
);

export function LooseleafBadge({
  tone = "neutral",
  className,
  children,
}: {
  tone?: VariantProps<typeof badgeVariants>["tone"];
  className?: string;
  children: ReactNode;
}) {
  return (
    <span className={cn(badgeVariants({ tone }), className)}>{children}</span>
  );
}

export function LooseleafPaper({
  className,
  children,
  ...props
}: ComponentPropsWithoutRef<"div">) {
  return (
    <div className={cn("ll-paper ll-stock-paper", className)} {...props}>
      {children}
    </div>
  );
}

export function LooseleafCard({
  className,
  children,
  ...props
}: ComponentPropsWithoutRef<"section">) {
  return (
    <section className={cn("ll-card", className)} {...props}>
      {children}
    </section>
  );
}

export const LooseleafDialog = {
  Root: DialogPrimitive.Root,
  Portal: DialogPrimitive.Portal,
  Overlay: DialogPrimitive.Overlay,
  Content: DialogPrimitive.Content,
  Title: DialogPrimitive.Title,
  Close: DialogPrimitive.Close,
};
