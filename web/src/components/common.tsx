import { Check, CheckCircle2, CircleDashed, Copy, XCircle } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { copyText } from "@/lib/clipboard";
import { formatDateTime, formatRelative } from "@/lib/format";
import { cn } from "@/lib/utils";

export function PageHeader({
  title,
  description,
  actions,
  children,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="mb-6 flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
      <div className="min-w-0">
        {children}
        <h1 className="truncate text-xl font-semibold tracking-tight sm:text-2xl">{title}</h1>
        {description && <p className="mt-1 text-sm text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}

type Tone = "success" | "danger" | "warning" | "neutral" | "info";

const toneClass: Record<Tone, string> = {
  success: "border-success/25 bg-success/10 text-success",
  danger: "border-destructive/25 bg-destructive/10 text-destructive",
  warning: "border-warning/30 bg-warning/10 text-warning",
  neutral: "border-border bg-muted text-muted-foreground",
  info: "border-brand/25 bg-brand/10 text-brand",
};

export function StatusBadge({
  tone,
  children,
  className,
  dot = true,
}: {
  tone: Tone;
  children: ReactNode;
  className?: string;
  dot?: boolean;
}) {
  return (
    <Badge variant="outline" className={cn("gap-1.5 font-medium", toneClass[tone], className)}>
      {dot && <span className="size-1.5 rounded-full bg-current" aria-hidden />}
      {children}
    </Badge>
  );
}

export function TestStatusBadge({ ok, at }: { ok: boolean | null; at: string | null }) {
  if (ok === null || !at) {
    return (
      <StatusBadge tone="neutral" dot={false}>
        <CircleDashed className="size-3" /> Not tested
      </StatusBadge>
    );
  }
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span tabIndex={0} className="inline-flex rounded-full outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
          {ok ? (
            <StatusBadge tone="success" dot={false}>
              <CheckCircle2 className="size-3" /> Passed
            </StatusBadge>
          ) : (
            <StatusBadge tone="danger" dot={false}>
              <XCircle className="size-3" /> Failed
            </StatusBadge>
          )}
        </span>
      </TooltipTrigger>
      <TooltipContent>Last tested {formatRelative(at)}</TooltipContent>
    </Tooltip>
  );
}

export function RelativeTime({ value, fallback = "never" }: { value: string | null | undefined; fallback?: string }) {
  if (!value) return <span className="text-muted-foreground">{fallback}</span>;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <time dateTime={value} className="cursor-default whitespace-nowrap">
          {formatRelative(value)}
        </time>
      </TooltipTrigger>
      <TooltipContent>{formatDateTime(value)}</TooltipContent>
    </Tooltip>
  );
}

export function CopyButton({
  value,
  label = "Copy",
  className,
  size = "icon-sm",
  variant = "ghost",
  onCopied,
}: {
  value: string;
  label?: string;
  className?: string;
  size?: "icon-sm" | "icon-xs" | "sm" | "default";
  variant?: "ghost" | "outline" | "default" | "secondary";
  onCopied?: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  const iconOnly = size.startsWith("icon");

  return (
    <Button
      type="button"
      variant={variant}
      size={size}
      className={className}
      aria-label={label}
      onClick={async () => {
        const ok = await copyText(value);
        if (!ok) {
          toast.error("Couldn't copy to clipboard");
          return;
        }
        setCopied(true);
        onCopied?.();
        window.clearTimeout(timer.current);
        timer.current = window.setTimeout(() => setCopied(false), 1600);
      }}
    >
      {copied ? <Check className="text-success" /> : <Copy />}
      {!iconOnly && (copied ? "Copied" : label)}
    </Button>
  );
}

export function Field({
  label,
  htmlFor,
  hint,
  error,
  children,
  className,
}: {
  label: ReactNode;
  htmlFor?: string;
  hint?: ReactNode;
  error?: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("grid gap-1.5", className)}>
      <label htmlFor={htmlFor} className="text-sm font-medium leading-none">
        {label}
      </label>
      {children}
      {error ? (
        <p className="text-xs text-destructive" role="alert">
          {error}
        </p>
      ) : hint ? (
        <p className="text-xs text-muted-foreground">{hint}</p>
      ) : null}
    </div>
  );
}

export function KeyValue({ label, children, mono }: { label: ReactNode; children: ReactNode; mono?: boolean }) {
  return (
    <div className="flex items-start justify-between gap-4 py-2.5 text-sm">
      <dt className="shrink-0 text-muted-foreground">{label}</dt>
      <dd className={cn("min-w-0 truncate text-right", mono && "font-mono text-xs leading-5")}>{children}</dd>
    </div>
  );
}
