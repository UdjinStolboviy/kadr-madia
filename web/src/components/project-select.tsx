import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useT } from "@/i18n/react";

const ALL = "__all";

/** Dropdown over a fixed set of values with an "all" option mapped to undefined. */
export function FilterSelect({
  value,
  onChange,
  options,
  allLabel,
  label,
  className,
  optionLabel,
}: {
  value: string | undefined;
  onChange: (v: string | undefined) => void;
  options: readonly string[];
  allLabel: string;
  label: string;
  className?: string;
  /** Optional display text for an option value (e.g. translated enum labels). */
  optionLabel?: (value: string) => string;
}) {
  return (
    <Select value={value ?? ALL} onValueChange={(v) => onChange(v === ALL ? undefined : v)}>
      <SelectTrigger size="sm" className={className ?? "w-36"} aria-label={label}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={ALL}>{allLabel}</SelectItem>
        {options.map((o) => (
          <SelectItem key={o} value={o}>
            {optionLabel ? optionLabel(o) : o}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

export function ProjectSelect({
  projects,
  value,
  onChange,
  loading,
}: {
  projects: { id: string; name: string; environment: string }[];
  value: string | undefined;
  onChange: (id: string) => void;
  loading?: boolean;
}) {
  const t = useT();
  return (
    <Select value={value ?? ""} onValueChange={onChange} disabled={loading || projects.length === 0}>
      <SelectTrigger size="sm" className="w-56" aria-label={t("projects.select.projectLabel")}>
        <SelectValue placeholder={loading ? t("projects.select.loading") : t("projects.select.project")} />
      </SelectTrigger>
      <SelectContent>
        {projects.map((p) => (
          <SelectItem key={p.id} value={p.id}>
            {p.name}
            <span className="text-xs text-muted-foreground uppercase">{p.environment}</span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
