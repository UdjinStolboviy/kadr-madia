import { Ban, ListChecks, RotateCcw, Wifi, WifiOff } from "lucide-react";
import { useCallback, useEffect, useState } from "react";

import { PageHeader, RelativeTime, StatusBadge } from "@/components/common";
import { FilterSelect } from "@/components/project-select";
import { EmptyState, ErrorState } from "@/components/states";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useT } from "@/i18n/react";
import { API_BASE, api } from "@/lib/api";
import { useCancelJob, useRetryJob } from "@/lib/queries";
import { JOB_POOLS, JOB_STATUSES, type Job } from "@/lib/types";

const LIMIT = 100;

type Tone = "success" | "danger" | "warning" | "neutral" | "info";
const STATUS_TONE: Record<string, Tone> = {
  queued: "neutral",
  running: "info",
  succeeded: "success",
  dead: "danger",
  cancelled: "warning",
};

const STATUS_KEY = {
  queued: "jobs.status.queued",
  running: "jobs.status.running",
  succeeded: "jobs.status.succeeded",
  dead: "jobs.status.dead",
  cancelled: "jobs.status.cancelled",
} as const;
const POOL_KEY = {
  image: "jobs.pool.image",
  video: "jobs.pool.video",
  maintenance: "jobs.pool.maintenance",
  webhook: "jobs.pool.webhook",
} as const;

interface Snapshot {
  key: string;
  items: Job[];
}

/**
 * Live job list: Server-Sent Events from /jobs/stream, falling back to polling
 * /jobs when the stream errors (proxies that buffer SSE, expired session, …).
 */
function useJobs(status: string | undefined, pool: string | undefined) {
  const key = `${status ?? ""}|${pool ?? ""}`;
  const [snap, setSnap] = useState<Snapshot | null>(null);
  const [mode, setMode] = useState<"live" | "polling">("live");
  const [error, setError] = useState<unknown>(null);
  const [tick, setTick] = useState(0);
  const refresh = useCallback(() => setTick((t) => t + 1), []);

  useEffect(() => {
    let stopped = false;
    let es: EventSource | null = null;
    let timer: number | undefined;
    const filters = { status, pool, limit: LIMIT };

    const poll = async () => {
      try {
        const r = await api.jobs.list(filters);
        if (stopped) return;
        setSnap({ key, items: r.items });
        setError(null);
      } catch (e) {
        if (!stopped) setError(e);
      }
    };
    const startPolling = () => {
      setMode("polling");
      void poll();
      timer = window.setInterval(() => void poll(), 3000);
    };

    if (typeof EventSource === "undefined") {
      startPolling();
    } else {
      const qs = new URLSearchParams({ limit: String(LIMIT) });
      if (status) qs.set("status", status);
      if (pool) qs.set("pool", pool);
      es = new EventSource(`${API_BASE}/jobs/stream?${qs}`, { withCredentials: true });
      es.addEventListener("jobs", (ev) => {
        try {
          const data = JSON.parse((ev as MessageEvent<string>).data) as { items: Job[] };
          setSnap({ key, items: data.items });
          setError(null);
          setMode("live");
        } catch {
          /* ignore malformed frame */
        }
      });
      es.onerror = () => {
        es?.close();
        es = null;
        if (!stopped && timer === undefined) startPolling();
      };
    }
    if (tick > 0) void poll();

    return () => {
      stopped = true;
      es?.close();
      window.clearInterval(timer);
    };
  }, [key, status, pool, tick]);

  const items = snap && snap.key === key ? snap.items : null;
  return { items, mode, error, refresh };
}

function Progress({ job }: { job: Job }) {
  if (job.progress === null || job.progress === undefined) {
    return <span className="text-muted-foreground">—</span>;
  }
  const pct = Math.max(0, Math.min(100, job.progress));
  return (
    <div className="flex items-center gap-2">
      <div
        className="h-1.5 w-20 overflow-hidden rounded-full bg-muted"
        role="progressbar"
        aria-valuenow={pct}
        aria-valuemin={0}
        aria-valuemax={100}
      >
        <div className="h-full rounded-full bg-brand transition-[width]" style={{ width: `${pct}%` }} />
      </div>
      <span className="w-9 text-xs text-muted-foreground tabular-nums">{pct}%</span>
    </div>
  );
}

type Pending = { job: Job; action: "retry" | "cancel" };

export default function JobsPage() {
  const t = useT();
  const statusLabel = (v: string) => (v in STATUS_KEY ? t(STATUS_KEY[v as keyof typeof STATUS_KEY]) : v);
  const poolLabel = (v: string) => (v in POOL_KEY ? t(POOL_KEY[v as keyof typeof POOL_KEY]) : v);
  const [status, setStatus] = useState<string | undefined>();
  const [pool, setPool] = useState<string | undefined>();
  const { items, mode, error, refresh } = useJobs(status, pool);
  const retry = useRetryJob();
  const cancel = useCancelJob();
  const [pending, setPending] = useState<Pending | null>(null);

  const confirm = () => {
    if (!pending) return;
    const m = pending.action === "retry" ? retry : cancel;
    m.mutate(pending.job.id, { onSettled: refresh });
    setPending(null);
  };

  return (
    <>
      <PageHeader
        title={t("jobs.title")}
        description={t("jobs.description")}
        actions={
          <>
            <span className="mr-1 inline-flex items-center gap-1.5 text-xs text-muted-foreground">
              {mode === "live" ? <Wifi className="size-3.5 text-success" /> : <WifiOff className="size-3.5" />}
              {mode === "live" ? t("jobs.live") : t("jobs.polling")}
            </span>
            <FilterSelect value={status} onChange={setStatus} options={JOB_STATUSES} allLabel={t("jobs.allStatuses")} label={t("jobs.status")} optionLabel={statusLabel} />
            <FilterSelect value={pool} onChange={setPool} options={JOB_POOLS} allLabel={t("jobs.allPools")} label={t("jobs.pool")} optionLabel={poolLabel} />
          </>
        }
      />

      {items === null ? (
        error ? (
          <ErrorState error={error} onRetry={refresh} />
        ) : (
          <div className="space-y-2 rounded-xl border bg-card p-4">
            {Array.from({ length: 6 }, (_, i) => (
              <Skeleton key={i} className="h-9 w-full" />
            ))}
          </div>
        )
      ) : items.length === 0 ? (
        <EmptyState
          icon={<ListChecks />}
          title={t("jobs.empty.title")}
          description={status || pool ? t("jobs.empty.filtered") : t("jobs.empty.desc")}
        />
      ) : (
        <>
          <div className="overflow-hidden rounded-xl border bg-card">
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="pl-4">{t("jobs.col.job")}</TableHead>
                  <TableHead>{t("jobs.status")}</TableHead>
                  <TableHead className="hidden sm:table-cell">{t("jobs.col.attempts")}</TableHead>
                  <TableHead className="hidden md:table-cell">{t("jobs.col.progress")}</TableHead>
                  <TableHead className="hidden xl:table-cell">{t("jobs.col.lastError")}</TableHead>
                  <TableHead className="hidden lg:table-cell">{t("jobs.col.created")}</TableHead>
                  <TableHead className="hidden lg:table-cell">{t("jobs.col.finished")}</TableHead>
                  <TableHead className="w-24 pr-4" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((j) => (
                  <TableRow key={j.id}>
                    <TableCell className="pl-4">
                      <div className="font-mono text-xs font-medium">{j.kind}</div>
                      <div className="text-xs text-muted-foreground">
                        {poolLabel(j.pool)} · <span className="font-mono">{j.id.slice(-8)}</span>
                      </div>
                    </TableCell>
                    <TableCell>
                      <StatusBadge tone={STATUS_TONE[j.status] ?? "neutral"}>{statusLabel(j.status)}</StatusBadge>
                    </TableCell>
                    <TableCell className="hidden text-sm tabular-nums sm:table-cell">
                      {j.attempts}/{j.max_attempts}
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      <Progress job={j} />
                    </TableCell>
                    <TableCell className="hidden max-w-[18rem] xl:table-cell">
                      {j.last_error ? (
                        <span className="block truncate text-xs text-destructive" title={j.last_error}>
                          {j.last_error}
                        </span>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </TableCell>
                    <TableCell className="hidden text-sm text-muted-foreground lg:table-cell">
                      <RelativeTime value={j.created_at} />
                    </TableCell>
                    <TableCell className="hidden text-sm text-muted-foreground lg:table-cell">
                      <RelativeTime value={j.finished_at} fallback="—" />
                    </TableCell>
                    <TableCell className="pr-3 text-right">
                      {(j.status === "dead" || j.status === "cancelled") && (
                        <Button variant="ghost" size="sm" onClick={() => setPending({ job: j, action: "retry" })}>
                          <RotateCcw /> {t("jobs.retry")}
                        </Button>
                      )}
                      {(j.status === "queued" || j.status === "running") && (
                        <Button
                          variant="ghost"
                          size="sm"
                          className="text-muted-foreground hover:text-destructive"
                          onClick={() => setPending({ job: j, action: "cancel" })}
                        >
                          <Ban /> {t("jobs.cancel")}
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
          <p className="mt-4 text-sm text-muted-foreground">
            {t("jobs.showing", { count: items.length })}
          </p>
        </>
      )}

      <AlertDialog open={!!pending} onOpenChange={(o) => !o && setPending(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{pending?.action === "retry" ? t("jobs.confirm.retryTitle") : t("jobs.confirm.cancelTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {pending?.action === "retry"
                ? t("jobs.confirm.retryDesc")
                : t("jobs.confirm.cancelDesc")}{" "}
              <span className="font-mono text-xs">{pending?.job.kind}</span>
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("jobs.confirm.back")}</AlertDialogCancel>
            <AlertDialogAction
              className={pending?.action === "cancel" ? "bg-destructive text-white hover:bg-destructive/90" : undefined}
              onClick={confirm}
            >
              {pending?.action === "retry" ? t("jobs.confirm.retryAction") : t("jobs.confirm.cancelAction")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
