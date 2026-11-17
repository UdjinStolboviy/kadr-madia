import { useQuery } from "@tanstack/react-query";
import { getRouteApi } from "@tanstack/react-router";
import { BarChart3, FolderKanban } from "lucide-react";
import { useCallback, useMemo, type ReactNode } from "react";

import { PageHeader } from "@/components/common";
import { ProjectSelect } from "@/components/project-select";
import { EmptyState, ErrorState } from "@/components/states";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { formatBytes } from "@/lib/format";
import { analyticsQuery } from "@/lib/queries";
import { useActiveProject } from "@/lib/use-active-project";
import { ANALYTICS_DAYS, type Analytics, type UsageDay } from "@/lib/types";

const route = getRouteApi("/app/analytics");

function Tile({ title, value, footer }: { title: string; value: ReactNode; footer?: ReactNode }) {
  return (
    <Card className="gap-2 py-4">
      <CardHeader className="px-5">
        <CardTitle className="text-sm font-medium text-muted-foreground">{title}</CardTitle>
      </CardHeader>
      <CardContent className="px-5">
        <div className="text-2xl font-semibold tracking-tight tabular-nums">{value}</div>
        {footer && <div className="mt-1 text-xs text-muted-foreground">{footer}</div>}
      </CardContent>
    </Card>
  );
}

const EMPTY_DAY: Omit<UsageDay, "day"> = {
  uploads: 0,
  assets_ready: 0,
  assets_failed: 0,
  bytes_in: 0,
  bytes_stored: 0,
  video_sec: 0,
  cpu_ms: 0,
  dedup_hits: 0,
};

/** The API only returns days with activity; fill the whole [from, to] range with zeros. */
function fillDays(a: Analytics): UsageDay[] {
  const byDay = new Map(a.days.map((d) => [d.day, d]));
  const out: UsageDay[] = [];
  const end = new Date(`${a.to}T00:00:00Z`).getTime();
  for (let t = new Date(`${a.from}T00:00:00Z`).getTime(); t <= end && out.length < 400; t += 86400_000) {
    const day = new Date(t).toISOString().slice(0, 10);
    out.push(byDay.get(day) ?? { day, ...EMPTY_DAY });
  }
  return out;
}

const W = 640;
const H = 180;
const PAD = { l: 44, r: 8, t: 8, b: 22 };
const innerW = W - PAD.l - PAD.r;
const innerH = H - PAD.t - PAD.b;

function niceMax(v: number) {
  if (v <= 0) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  const n = v / p;
  return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10) * p;
}

function Axes({ days, max, fmt }: { days: UsageDay[]; max: number; fmt: (v: number) => string }) {
  const ticks = [0, 0.5, 1];
  const step = Math.max(1, Math.ceil(days.length / 6));
  return (
    <g className="text-muted-foreground" fontSize="10" fill="currentColor">
      {ticks.map((t) => {
        const y = PAD.t + innerH * (1 - t);
        return (
          <g key={t}>
            <line x1={PAD.l} x2={W - PAD.r} y1={y} y2={y} stroke="currentColor" strokeOpacity={t === 0 ? 0.4 : 0.15} />
            <text x={PAD.l - 6} y={y + 3} textAnchor="end">
              {fmt(max * t)}
            </text>
          </g>
        );
      })}
      {days.map((d, i) =>
        i % step === 0 ? (
          <text key={d.day} x={PAD.l + (innerW * (i + 0.5)) / days.length} y={H - 6} textAnchor="middle">
            {d.day.slice(5)}
          </text>
        ) : null,
      )}
    </g>
  );
}

function Legend({ items }: { items: { color: string; label: string }[] }) {
  return (
    <div className="flex flex-wrap gap-4 text-xs text-muted-foreground">
      {items.map((i) => (
        <span key={i.label} className="inline-flex items-center gap-1.5">
          <span className="size-2.5 rounded-sm" style={{ backgroundColor: i.color }} />
          {i.label}
        </span>
      ))}
    </div>
  );
}

function UploadsChart({ days }: { days: UsageDay[] }) {
  const max = niceMax(Math.max(...days.map((d) => d.uploads)));
  const bw = innerW / days.length;
  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="w-full" role="img" aria-label="Uploads per day">
      <Axes days={days} max={max} fmt={(v) => String(Math.round(v))} />
      {days.map((d, i) => {
        const h = (d.uploads / max) * innerH;
        return (
          <rect
            key={d.day}
            x={PAD.l + i * bw + bw * 0.15}
            y={PAD.t + innerH - h}
            width={bw * 0.7}
            height={h}
            rx={Math.min(2, bw * 0.1)}
            fill="var(--brand)"
          >
            <title>{`${d.day}: ${d.uploads} upload${d.uploads === 1 ? "" : "s"}`}</title>
          </rect>
        );
      })}
    </svg>
  );
}

function BytesChart({ days }: { days: UsageDay[] }) {
  const max = niceMax(Math.max(...days.map((d) => d.bytes_in), ...days.map((d) => d.bytes_stored)));
  const x = (i: number) => PAD.l + (innerW * (i + 0.5)) / days.length;
  const y = (v: number) => PAD.t + innerH * (1 - v / max);
  const line = (pick: (d: UsageDay) => number) => days.map((d, i) => `${i === 0 ? "M" : "L"}${x(i).toFixed(1)},${y(pick(d)).toFixed(1)}`).join(" ");
  const area = `${line((d) => d.bytes_stored)} L${x(days.length - 1)},${y(0)} L${x(0)},${y(0)} Z`;
  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="w-full" role="img" aria-label="Bytes ingested versus stored per day">
      <Axes days={days} max={max} fmt={(v) => formatBytes(v, 0)} />
      <path d={area} fill="var(--success)" fillOpacity={0.15} />
      <path d={line((d) => d.bytes_in)} fill="none" stroke="var(--brand)" strokeWidth={2} strokeLinejoin="round" />
      <path d={line((d) => d.bytes_stored)} fill="none" stroke="var(--success)" strokeWidth={2} strokeLinejoin="round" />
      {days.map((d, i) => (
        <rect key={d.day} x={PAD.l + (innerW * i) / days.length} y={PAD.t} width={innerW / days.length} height={innerH} fill="transparent">
          <title>{`${d.day}: in ${formatBytes(d.bytes_in)}, stored ${formatBytes(d.bytes_stored)}`}</title>
        </rect>
      ))}
    </svg>
  );
}

function ChartCard({ title, legend, children }: { title: string; legend: { color: string; label: string }[]; children: ReactNode }) {
  return (
    <Card className="gap-3 py-5">
      <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2 px-5">
        <CardTitle className="text-sm font-medium">{title}</CardTitle>
        <Legend items={legend} />
      </CardHeader>
      <CardContent className="px-5">{children}</CardContent>
    </Card>
  );
}

function pct(v: number) {
  return `${(v * 100).toFixed(1)}%`;
}

function Dashboard({ data }: { data: Analytics }) {
  const days = useMemo(() => fillDays(data), [data]);
  const t = data.totals;
  const empty = t.uploads === 0 && t.bytes_in === 0 && t.assets_ready === 0 && t.assets_failed === 0;
  if (empty) {
    return (
      <EmptyState icon={<BarChart3 />} title="No activity in this period" description="Usage appears here after media is uploaded and processed." />
    );
  }
  return (
    <div className="grid gap-4">
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Tile title="Ingested" value={formatBytes(t.bytes_in)} footer={`${t.uploads} uploads`} />
        <Tile title="Stored" value={formatBytes(t.bytes_stored)} footer="After optimisation" />
        <Tile title="Savings" value={pct(data.savings_ratio)} footer={`${formatBytes(Math.max(0, t.bytes_in - t.bytes_stored))} saved`} />
        <Tile title="Assets ready" value={t.assets_ready.toLocaleString()} footer={`${t.assets_failed.toLocaleString()} failed`} />
        <Tile title="Failed rate" value={pct(data.failed_rate)} footer={`${t.assets_failed} of ${t.assets_ready + t.assets_failed} finished`} />
        <Tile title="Video minutes" value={(t.video_sec / 60).toFixed(1)} footer={`${(t.cpu_ms / 1000).toFixed(0)} s CPU`} />
        <Tile title="Dedup hits" value={t.dedup_hits.toLocaleString()} footer="Uploads reusing an existing blob" />
      </div>
      <ChartCard title="Uploads per day" legend={[{ color: "var(--brand)", label: "Uploads" }]}>
        <UploadsChart days={days} />
      </ChartCard>
      <ChartCard
        title="Bytes in vs stored"
        legend={[
          { color: "var(--brand)", label: "Ingested" },
          { color: "var(--success)", label: "Stored" },
        ]}
      >
        <BytesChart days={days} />
      </ChartCard>
    </div>
  );
}

export default function AnalyticsPage() {
  const { project, days = 30 } = route.useSearch();
  const navigate = route.useNavigate();
  const setProject = useCallback(
    (id: string) => void navigate({ search: (s) => ({ ...s, project: id }), replace: true }),
    [navigate],
  );
  const { projects, list, active } = useActiveProject(project, setProject);
  const analytics = useQuery({ ...analyticsQuery(active?.id ?? "", days), enabled: !!active });

  return (
    <>
      <PageHeader
        title="Analytics"
        description="Usage, storage savings and processing health."
        actions={
          <>
            <ProjectSelect projects={list} value={active?.id} onChange={setProject} loading={projects.isPending} />
            <Select
              value={String(days)}
              onValueChange={(v) => void navigate({ search: (s) => ({ ...s, days: Number(v) as 7 | 30 | 90 }), replace: true })}
            >
              <SelectTrigger size="sm" className="w-40" aria-label="Period">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ANALYTICS_DAYS.map((d) => (
                  <SelectItem key={d} value={String(d)}>
                    Last {d} days
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </>
        }
      />
      {projects.isError ? (
        <ErrorState error={projects.error} onRetry={() => void projects.refetch()} />
      ) : projects.isSuccess && list.length === 0 ? (
        <EmptyState icon={<FolderKanban />} title="No projects yet" description="Create a project to see its analytics." />
      ) : !active || analytics.isPending ? (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          {Array.from({ length: 4 }, (_, i) => (
            <Skeleton key={i} className="h-28 w-full" />
          ))}
        </div>
      ) : analytics.isError ? (
        <ErrorState error={analytics.error} onRetry={() => void analytics.refetch()} />
      ) : (
        <Dashboard data={analytics.data} />
      )}
    </>
  );
}
