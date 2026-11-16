import { StatusBadge } from "@/components/common";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { formatBytes } from "@/lib/format";
import { NO_STORAGE } from "@/lib/forms";
import type { Environment, StorageProfile } from "@/lib/types";
import { cn } from "@/lib/utils";

export function StorageSelect({
  id,
  value,
  onChange,
  profiles,
  loading,
}: {
  id?: string;
  value: string;
  onChange: (v: string) => void;
  profiles: StorageProfile[];
  loading?: boolean;
}) {
  return (
    <Select value={value} onValueChange={onChange} disabled={loading}>
      <SelectTrigger id={id} className="w-full">
        <SelectValue placeholder={loading ? "Loading…" : "Select storage"} />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={NO_STORAGE}>Platform default</SelectItem>
        {profiles.map((p) => (
          <SelectItem key={p.id} value={p.id}>
            {p.name}
            <span className="text-xs text-muted-foreground uppercase">{p.provider}</span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

export function EnvBadge({ env }: { env: Environment }) {
  return env === "live" ? (
    <StatusBadge tone="success">live</StatusBadge>
  ) : (
    <StatusBadge tone="warning">test</StatusBadge>
  );
}

export function UsageBar({ used, quota }: { used: number; quota: number | null }) {
  const pct = quota ? Math.min(100, (used / quota) * 100) : 0;
  return (
    <div className="min-w-[8rem]">
      <div className="text-sm tabular-nums">
        {formatBytes(used)}
        <span className="text-muted-foreground"> / {quota ? formatBytes(quota) : "∞"}</span>
      </div>
      {quota ? (
        <div className="mt-1.5 h-1 w-full overflow-hidden rounded-full bg-muted">
          <div
            className={cn("h-full rounded-full", pct > 90 ? "bg-destructive" : pct > 75 ? "bg-warning" : "bg-brand")}
            style={{ width: `${Math.max(pct, 1)}%` }}
          />
        </div>
      ) : null}
    </div>
  );
}

