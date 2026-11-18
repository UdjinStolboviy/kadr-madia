import { useQuery } from "@tanstack/react-query";
import { CalendarClock, Loader2, MoreHorizontal, Pencil, Play, Plus, Trash2, Zap } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import { Field, RelativeTime, StatusBadge } from "@/components/common";
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import type { MessageKey } from "@/i18n/dictionaries";
import { useT } from "@/i18n/react";
import { errorMessage, isApiError } from "@/lib/api";
import {
  agentsQuery,
  runsQuery,
  schedulesQuery,
  useDeleteSchedule,
  useRunSchedule,
  useSaveSchedule,
  useStartRun,
  useToggleSchedule,
} from "@/lib/queries";
import { SCHEDULE_FREQUENCIES, type AgentRun, type AgentRunStatus, type AgentSchedule, type ScheduleFrequency } from "@/lib/types";
import { useRole } from "@/lib/use-role";
import { cn } from "@/lib/utils";

type Tone = "success" | "danger" | "warning" | "neutral" | "info";
const RUN_STATUS: Record<AgentRunStatus, { tone: Tone; label: MessageKey }> = {
  queued: { tone: "neutral", label: "agents.runs.status.queued" },
  running: { tone: "info", label: "agents.runs.status.running" },
  succeeded: { tone: "success", label: "agents.runs.status.succeeded" },
  failed: { tone: "danger", label: "agents.runs.status.failed" },
};
const TEMPLATES = ["moderation", "health", "cost"] as const;
const WEEKDAYS = [1, 2, 3, 4, 5, 6, 0] as const;
const TIME_RE = /^([01][0-9]|2[0-3]):[0-5][0-9]$/;

/** "03:00" UTC → the viewer's local "05:00" (today's offset). */
function localTime(hhmm: string): string {
  if (!TIME_RE.test(hhmm)) return "";
  const [h, m] = hhmm.split(":").map(Number) as [number, number];
  const d = new Date();
  d.setUTCHours(h, m, 0, 0);
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function useWhen() {
  const t = useT();
  return (s: Pick<AgentSchedule, "frequency" | "time_utc" | "weekday">) => {
    if (s.frequency === "hourly") return t("agents.schedules.every.hourly", { mm: s.time_utc.slice(3) });
    if (s.frequency === "weekly")
      return t("agents.schedules.every.weekly", { day: t(`agents.schedules.weekday.${s.weekday}` as MessageKey), time: s.time_utc });
    return t("agents.schedules.every.daily", { time: s.time_utc });
  };
}

export function SchedulesTab() {
  const t = useT();
  const { canManage } = useRole();
  const schedules = useQuery(schedulesQuery);
  const runs = useQuery(runsQuery);
  const when = useWhen();
  const toggle = useToggleSchedule();
  const runNow = useRunSchedule();
  const remove = useDeleteSchedule();
  const [editing, setEditing] = useState<AgentSchedule | "new" | "adhoc" | null>(null);
  const [deleting, setDeleting] = useState<AgentSchedule | null>(null);
  const [detail, setDetail] = useState<AgentRun | null>(null);

  if (schedules.isPending) {
    return (
      <div className="space-y-2 rounded-xl border bg-card p-4">
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-12 w-full" />
      </div>
    );
  }
  if (schedules.isError) return <ErrorState error={schedules.error} onRetry={() => void schedules.refetch()} />;
  const ai = schedules.data.ai;
  const list = schedules.data.items;
  const canRun = canManage && ai.agent_runs;

  return (
    <div className="grid gap-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 max-w-2xl">
          <h2 className="text-sm font-semibold">{t("agents.schedules.title")}</h2>
          <p className="mt-0.5 text-sm text-muted-foreground">{t("agents.schedules.desc")}</p>
          {ai.model && <p className="mt-1 text-xs text-muted-foreground">{t("agents.schedules.model", { model: ai.model })}</p>}
        </div>
        {canManage && (
          <div className="flex gap-2">
            <Button variant="outline" disabled={!canRun} onClick={() => setEditing("adhoc")}>
              <Zap /> {t("agents.schedules.runAdhoc")}
            </Button>
            <Button onClick={() => setEditing("new")}>
              <Plus /> {t("agents.schedules.new")}
            </Button>
          </div>
        )}
      </div>
      {!ai.agent_runs && (
        <p className="rounded-lg border border-warning/40 bg-warning/5 px-3 py-2 text-sm text-warning">{t("agents.schedules.aiOff")}</p>
      )}

      {list.length === 0 ? (
        <EmptyState icon={<CalendarClock />} title={t("agents.schedules.emptyTitle")} description={t("agents.schedules.emptyDesc")} />
      ) : (
        <div className="overflow-hidden rounded-xl border bg-card">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="pl-4">{t("agents.schedules.col.name")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("agents.schedules.col.agent")}</TableHead>
                <TableHead>{t("agents.schedules.col.when")}</TableHead>
                <TableHead className="hidden lg:table-cell">{t("agents.schedules.col.next")}</TableHead>
                <TableHead className="hidden sm:table-cell">{t("agents.schedules.col.last")}</TableHead>
                <TableHead>{t("agents.schedules.col.enabled")}</TableHead>
                <TableHead className="w-10" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.map((s) => (
                <TableRow key={s.id} className={cn(!s.enabled && "opacity-60")}>
                  <TableCell className="pl-4">
                    <div className="font-medium">{s.name}</div>
                    <div className="max-w-[22rem] truncate text-xs text-muted-foreground" title={s.prompt}>
                      {s.prompt}
                    </div>
                  </TableCell>
                  <TableCell className="hidden text-sm md:table-cell">{s.agent_name || s.agent_id}</TableCell>
                  <TableCell className="text-sm">{when(s)}</TableCell>
                  <TableCell className="hidden text-sm lg:table-cell">
                    <RelativeTime value={s.next_run_at} />
                  </TableCell>
                  <TableCell className="hidden text-sm sm:table-cell">
                    <RelativeTime value={s.last_run_at} />
                  </TableCell>
                  <TableCell>
                    <Switch
                      checked={s.enabled}
                      disabled={!canManage || toggle.isPending}
                      onCheckedChange={(v) => toggle.mutate({ id: s.id, enabled: v })}
                      aria-label={t("agents.schedules.form.enabled")}
                    />
                  </TableCell>
                  <TableCell className="pr-3">
                    {canManage && (
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <Button variant="ghost" size="icon-sm" aria-label={t("agents.schedules.menu", { name: s.name })}>
                            <MoreHorizontal />
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          <DropdownMenuItem disabled={!canRun} onSelect={() => runNow.mutate(s.id)}>
                            <Play /> {t("agents.schedules.runNow")}
                          </DropdownMenuItem>
                          <DropdownMenuItem onSelect={() => setEditing(s)}>
                            <Pencil /> {t("agents.schedules.edit")}
                          </DropdownMenuItem>
                          <DropdownMenuSeparator />
                          <DropdownMenuItem variant="destructive" onSelect={() => setDeleting(s)}>
                            <Trash2 /> {t("agents.schedules.delete")}
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      <section className="grid gap-3">
        <h2 className="text-sm font-semibold">{t("agents.runs.title")}</h2>
        {runs.isPending ? (
          <Skeleton className="h-24 w-full rounded-xl" />
        ) : runs.isError ? (
          <ErrorState error={runs.error} onRetry={() => void runs.refetch()} />
        ) : runs.data.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("agents.runs.empty")}</p>
        ) : (
          <div className="overflow-hidden rounded-xl border bg-card">
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="pl-4">{t("agents.runs.col.status")}</TableHead>
                  <TableHead>{t("agents.runs.col.agent")}</TableHead>
                  <TableHead className="hidden sm:table-cell">{t("agents.runs.col.trigger")}</TableHead>
                  <TableHead className="hidden md:table-cell">{t("agents.runs.col.tools")}</TableHead>
                  <TableHead>{t("agents.runs.col.started")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {runs.data.map((r) => {
                  const st = RUN_STATUS[r.status];
                  return (
                    <TableRow key={r.id} className="cursor-pointer" onClick={() => setDetail(r)}>
                      <TableCell className="pl-4">
                        <StatusBadge tone={st.tone}>
                          {(r.status === "running" || r.status === "queued") && <Loader2 className="size-3 animate-spin" />}
                          {t(st.label)}
                        </StatusBadge>
                      </TableCell>
                      <TableCell className="text-sm">
                        <div>{r.agent_name || r.agent_id}</div>
                        <div className="max-w-[20rem] truncate text-xs text-muted-foreground">{r.prompt}</div>
                      </TableCell>
                      <TableCell className="hidden text-sm sm:table-cell">{t(`agents.runs.trigger.${r.trigger}` as MessageKey)}</TableCell>
                      <TableCell className="hidden text-sm tabular-nums md:table-cell">{r.tool_calls}</TableCell>
                      <TableCell className="text-sm">
                        <RelativeTime value={r.started_at ?? r.created_at} />
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </div>
        )}
      </section>

      <ScheduleSheet
        target={editing}
        onClose={() => setEditing(null)}
        canRun={canRun}
      />
      <RunSheet run={detail ? (runs.data?.find((r) => r.id === detail.id) ?? detail) : null} onClose={() => setDetail(null)} />

      <AlertDialog open={!!deleting} onOpenChange={(o) => !o && setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("agents.schedules.deleteTitle", { name: deleting?.name ?? "" })}</AlertDialogTitle>
            <AlertDialogDescription>{t("agents.schedules.deleteDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("agents.schedules.form.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => {
                if (deleting) remove.mutate(deleting.id);
                setDeleting(null);
              }}
            >
              {t("agents.schedules.delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

interface FormState {
  name: string;
  agentId: string;
  prompt: string;
  frequency: ScheduleFrequency;
  time: string;
  weekday: number;
  maxTurns: string;
  notify: boolean;
  enabled: boolean;
}

function ScheduleSheet({ target, onClose, canRun }: { target: AgentSchedule | "new" | "adhoc" | null; onClose: () => void; canRun: boolean }) {
  return (
    <Sheet open={!!target} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full gap-0 sm:max-w-xl">
        {target && (
          <ScheduleForm
            key={typeof target === "string" ? target : target.id}
            schedule={typeof target === "string" ? undefined : target}
            adhoc={target === "adhoc"}
            canRun={canRun}
            onClose={onClose}
          />
        )}
      </SheetContent>
    </Sheet>
  );
}

function ScheduleForm({ schedule, adhoc, canRun, onClose }: { schedule?: AgentSchedule; adhoc: boolean; canRun: boolean; onClose: () => void }) {
  const t = useT();
  const agents = useQuery(agentsQuery);
  const save = useSaveSchedule();
  const start = useStartRun();
  const [s, setS] = useState<FormState>(() => ({
    name: schedule?.name ?? "",
    agentId: schedule?.agent_id ?? "",
    prompt: schedule?.prompt ?? "",
    frequency: schedule?.frequency ?? "daily",
    time: schedule?.time_utc ?? "03:00",
    weekday: schedule?.weekday ?? 1,
    maxTurns: String(schedule?.max_turns ?? 30),
    notify: schedule?.notify ?? true,
    enabled: schedule?.enabled ?? true,
  }));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => setS((p) => ({ ...p, [k]: v }));
  const active = (agents.data ?? []).filter((a) => a.status === "active" || a.id === s.agentId);
  const pending = save.isPending || start.isPending;

  function validate() {
    const e: Record<string, string> = {};
    if (!adhoc && (!s.name.trim() || s.name.trim().length > 100)) e.name = t("agents.schedules.form.errName");
    if (!s.agentId) e.agent_id = t("agents.schedules.form.errAgent");
    if (!s.prompt.trim()) e.prompt = t("agents.schedules.form.errPrompt");
    if (!TIME_RE.test(s.time)) e.time_utc = "HH:MM";
    const n = Number(s.maxTurns);
    if (!Number.isInteger(n) || n < 1 || n > 100) e.max_turns = "1–100";
    return e;
  }

  async function submit(ev: React.FormEvent) {
    ev.preventDefault();
    const e = validate();
    setErrors(e);
    if (Object.keys(e).length) return;
    try {
      if (adhoc) {
        await start.mutateAsync({ agent_id: s.agentId, prompt: s.prompt.trim() });
      } else {
        await save.mutateAsync({
          id: schedule?.id,
          body: {
            name: s.name.trim(),
            agent_id: s.agentId,
            prompt: s.prompt.trim(),
            frequency: s.frequency,
            time_utc: s.time,
            weekday: s.weekday,
            max_turns: Number(s.maxTurns),
            notify: s.notify,
            enabled: s.enabled,
          },
        });
      }
      onClose();
    } catch (err) {
      if (isApiError(err, "validation_failed") && Object.keys(err.fieldErrors).length) setErrors(err.fieldErrors);
      else toast.error(adhoc ? t("agents.runs.startFailed") : t("agents.schedules.saveFailed"), { description: errorMessage(err) });
    }
  }

  const title = adhoc
    ? t("agents.schedules.form.adhocTitle")
    : schedule
      ? t("agents.schedules.form.editTitle", { name: schedule.name })
      : t("agents.schedules.form.createTitle");

  return (
    <form onSubmit={submit} noValidate className="flex min-h-0 flex-1 flex-col">
      <SheetHeader>
        <SheetTitle>{title}</SheetTitle>
        <SheetDescription>{t("agents.schedules.desc")}</SheetDescription>
      </SheetHeader>
      <div className="grid flex-1 gap-5 overflow-y-auto px-4 pb-4">
        {!adhoc && (
          <Field label={t("agents.schedules.form.name")} htmlFor="sch-name" error={errors.name}>
            <Input
              id="sch-name"
              value={s.name}
              placeholder={t("agents.schedules.form.namePh")}
              aria-invalid={!!errors.name}
              onChange={(e) => set("name", e.target.value)}
            />
          </Field>
        )}
        <Field label={t("agents.schedules.form.agent")} htmlFor="sch-agent" hint={t("agents.schedules.form.agentHint")} error={errors.agent_id}>
          {active.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("agents.schedules.form.noAgents")}</p>
          ) : (
            <Select value={s.agentId} onValueChange={(v) => set("agentId", v)}>
              <SelectTrigger id="sch-agent" className="w-full">
                <SelectValue placeholder={t("agents.schedules.form.agentPh")} />
              </SelectTrigger>
              <SelectContent>
                {active.map((a) => (
                  <SelectItem key={a.id} value={a.id}>
                    {a.name} · {a.level}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </Field>
        <Field label={t("agents.schedules.form.prompt")} htmlFor="sch-prompt" error={errors.prompt}>
          <div className="grid gap-2">
            <div className="flex flex-wrap items-center gap-1.5">
              <span className="text-xs text-muted-foreground">{t("agents.schedules.form.templates")}:</span>
              {TEMPLATES.map((k) => (
                <Button
                  key={k}
                  type="button"
                  size="xs"
                  variant="outline"
                  onClick={() => {
                    const text = t(`agents.schedules.tplText.${k}` as MessageKey);
                    setS((p) => ({ ...p, prompt: text, name: p.name || t(`agents.schedules.tpl.${k}` as MessageKey) }));
                  }}
                >
                  {t(`agents.schedules.tpl.${k}` as MessageKey)}
                </Button>
              ))}
            </div>
            <Textarea
              id="sch-prompt"
              rows={6}
              value={s.prompt}
              placeholder={t("agents.schedules.form.promptPh")}
              aria-invalid={!!errors.prompt}
              onChange={(e) => set("prompt", e.target.value)}
            />
          </div>
        </Field>
        {!adhoc && (
          <>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={t("agents.schedules.form.frequency")} htmlFor="sch-freq">
                <Select value={s.frequency} onValueChange={(v) => set("frequency", v as ScheduleFrequency)}>
                  <SelectTrigger id="sch-freq" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {SCHEDULE_FREQUENCIES.map((f) => (
                      <SelectItem key={f} value={f}>
                        {t(`agents.schedules.freq.${f}` as MessageKey)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              {s.frequency === "hourly" ? (
                <Field label={t("agents.schedules.form.minute")} htmlFor="sch-minute" error={errors.time_utc}>
                  <Input
                    id="sch-minute"
                    type="number"
                    min={0}
                    max={59}
                    value={Number(s.time.slice(3))}
                    onChange={(e) => {
                      const m = Math.min(59, Math.max(0, Number(e.target.value) || 0));
                      set("time", `00:${String(m).padStart(2, "0")}`);
                    }}
                  />
                </Field>
              ) : (
                <Field
                  label={t("agents.schedules.form.time")}
                  htmlFor="sch-time"
                  hint={localTime(s.time) ? t("agents.schedules.form.timeHint", { local: localTime(s.time) }) : undefined}
                  error={errors.time_utc}
                >
                  <Input id="sch-time" type="time" value={s.time} onChange={(e) => set("time", e.target.value)} />
                </Field>
              )}
            </div>
            {s.frequency === "weekly" && (
              <Field label={t("agents.schedules.form.weekday")} htmlFor="sch-weekday">
                <Select value={String(s.weekday)} onValueChange={(v) => set("weekday", Number(v))}>
                  <SelectTrigger id="sch-weekday" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {WEEKDAYS.map((d) => (
                      <SelectItem key={d} value={String(d)}>
                        {t(`agents.schedules.weekday.${d}` as MessageKey)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            )}
            <Field label={t("agents.schedules.form.maxTurns")} htmlFor="sch-turns" hint={t("agents.schedules.form.maxTurnsHint")} error={errors.max_turns}>
              <Input id="sch-turns" type="number" min={1} max={100} value={s.maxTurns} onChange={(e) => set("maxTurns", e.target.value)} />
            </Field>
            <label className="flex items-center justify-between gap-3 text-sm">
              <span>{t("agents.schedules.form.notify")}</span>
              <Switch checked={s.notify} onCheckedChange={(v) => set("notify", v)} />
            </label>
            <label className="flex items-center justify-between gap-3 text-sm">
              <span>{t("agents.schedules.form.enabled")}</span>
              <Switch checked={s.enabled} onCheckedChange={(v) => set("enabled", v)} />
            </label>
          </>
        )}
      </div>
      <SheetFooter className="flex-row justify-end border-t">
        <Button type="button" variant="outline" onClick={onClose}>
          {t("agents.schedules.form.cancel")}
        </Button>
        <Button type="submit" disabled={pending || (adhoc && !canRun)}>
          {pending && <Loader2 className="animate-spin" />}
          {adhoc ? t("agents.schedules.form.run") : t("agents.schedules.form.save")}
        </Button>
      </SheetFooter>
    </form>
  );
}

function RunSheet({ run, onClose }: { run: AgentRun | null; onClose: () => void }) {
  const t = useT();
  const st = run ? RUN_STATUS[run.status] : null;
  return (
    <Sheet open={!!run} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full gap-0 sm:max-w-2xl">
        {run && st && (
          <>
            <SheetHeader>
              <SheetTitle className="flex items-center gap-2">
                {t("agents.runs.detail.title", { id: run.id })}
                <StatusBadge tone={st.tone}>{t(st.label)}</StatusBadge>
              </SheetTitle>
              <SheetDescription>
                {run.agent_name || run.agent_id} · {t(`agents.runs.trigger.${run.trigger}` as MessageKey)} ·{" "}
                <RelativeTime value={run.started_at ?? run.created_at} />
              </SheetDescription>
            </SheetHeader>
            <div className="grid flex-1 content-start gap-5 overflow-y-auto px-4 pb-6">
              <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
                <span>{t("agents.runs.detail.toolCalls", { count: run.tool_calls })}</span>
                <span>{t("agents.runs.detail.tokens", { input: run.input_tokens.toLocaleString(), output: run.output_tokens.toLocaleString() })}</span>
                {run.model && (
                  <span>
                    {t("agents.runs.detail.model")}: <code className="font-mono">{run.model}</code>
                  </span>
                )}
              </div>
              <div className="grid gap-1.5">
                <span className="text-sm font-medium">{t("agents.runs.detail.prompt")}</span>
                <p className="rounded-lg border bg-muted/40 p-3 text-sm whitespace-pre-wrap">{run.prompt}</p>
              </div>
              {run.error && (
                <div className="grid gap-1.5">
                  <span className="text-sm font-medium text-destructive">{t("agents.runs.detail.error")}</span>
                  <pre className="rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-xs whitespace-pre-wrap">{run.error}</pre>
                </div>
              )}
              <div className="grid gap-1.5">
                <span className="text-sm font-medium">{t("agents.runs.detail.output")}</span>
                {run.output ? (
                  <pre className="rounded-lg border bg-card p-3 font-sans text-sm leading-relaxed whitespace-pre-wrap">{run.output}</pre>
                ) : (
                  <p className="text-sm text-muted-foreground">{t("agents.runs.detail.noOutput")}</p>
                )}
              </div>
            </div>
            <SheetFooter className="border-t">
              <Button variant="outline" onClick={onClose}>
                {t("common.close")}
              </Button>
            </SheetFooter>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}
