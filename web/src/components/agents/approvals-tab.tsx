import { useQuery } from "@tanstack/react-query";
import { Check, ChevronDown, Inbox, Loader2, X } from "lucide-react";
import { useState } from "react";

import { RelativeTime, StatusBadge } from "@/components/common";
import { EmptyState, ErrorState } from "@/components/states";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import type { MessageKey } from "@/i18n/dictionaries";
import { useT } from "@/i18n/react";
import { formatRelative } from "@/lib/format";
import { approvalsQuery, projectsQuery, useDecideApproval } from "@/lib/queries";
import type { AgentApproval, ApprovalStatus } from "@/lib/types";
import { useRole } from "@/lib/use-role";
import { cn } from "@/lib/utils";

type Tone = "success" | "danger" | "warning" | "neutral" | "info";
const STATUS: Record<ApprovalStatus, { tone: Tone; label: MessageKey }> = {
  pending: { tone: "warning", label: "agents.approvals.status.pending" },
  executed: { tone: "success", label: "agents.approvals.status.executed" },
  failed: { tone: "danger", label: "agents.approvals.status.failed" },
  rejected: { tone: "neutral", label: "agents.approvals.status.rejected" },
  expired: { tone: "neutral", label: "agents.approvals.status.expired" },
};

function methodTone(m: string) {
  if (m === "DELETE") return "text-destructive";
  if (m === "GET") return "text-muted-foreground";
  return "text-brand";
}

function Json({ value }: { value: unknown }) {
  if (value === null || value === undefined) return null;
  return (
    <pre className="max-h-56 overflow-auto rounded-md bg-muted/60 p-2.5 font-mono text-xs leading-relaxed whitespace-pre-wrap">
      {typeof value === "string" ? value : JSON.stringify(value, null, 2)}
    </pre>
  );
}

function ApprovalCard({
  a,
  projectName,
  canDecide,
  onDecide,
}: {
  a: AgentApproval;
  projectName?: string;
  canDecide: boolean;
  onDecide: (a: AgentApproval, approve: boolean) => void;
}) {
  const t = useT();
  const pending = a.status === "pending";
  const [open, setOpen] = useState(pending);
  const st = STATUS[a.status] ?? STATUS.expired;
  return (
    <div className={cn("rounded-xl border bg-card", pending && "border-warning/40")}>
      <button
        type="button"
        className="flex w-full items-start gap-3 p-4 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
      >
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <StatusBadge tone={st.tone}>{t(st.label)}</StatusBadge>
            <span className="font-medium">{a.agent_name ?? a.agent_id}</span>
            <span className="rounded bg-secondary px-1.5 py-0.5 font-mono text-[11px]">{a.action}</span>
            {projectName && <span className="text-xs text-muted-foreground">{projectName}</span>}
          </div>
          <div className="mt-1.5 truncate font-mono text-xs">
            <span className={cn("font-semibold", methodTone(a.method))}>{a.method}</span> {a.path}
          </div>
          <p className="mt-1.5 line-clamp-2 text-sm text-muted-foreground">“{a.reason}”</p>
        </div>
        <div className="flex shrink-0 flex-col items-end gap-1 text-xs text-muted-foreground">
          <RelativeTime value={a.created_at} />
          <ChevronDown className={cn("size-4 transition-transform", open && "rotate-180")} />
        </div>
      </button>
      {open && (
        <div className="grid gap-3 border-t px-4 py-3 text-sm">
          <dl className="grid gap-1.5 sm:grid-cols-[10rem_1fr]">
            <dt className="text-muted-foreground">{t("agents.approvals.reason")}</dt>
            <dd className="whitespace-pre-wrap">{a.reason}</dd>
            {a.tool && (
              <>
                <dt className="text-muted-foreground">{t("agents.approvals.tool")}</dt>
                <dd className="font-mono text-xs">{a.tool}</dd>
              </>
            )}
            <dt className="text-muted-foreground">{t("agents.approvals.permission")}</dt>
            <dd className="font-mono text-xs">{a.action}</dd>
            {a.decision_note && (
              <>
                <dt className="text-muted-foreground">{t("agents.approvals.decidedNote")}</dt>
                <dd>{a.decision_note}</dd>
              </>
            )}
          </dl>
          {a.body !== null && a.body !== undefined && (
            <div className="grid gap-1">
              <span className="text-xs font-medium text-muted-foreground">{t("agents.approvals.body")}</span>
              <Json value={a.body} />
            </div>
          )}
          {a.result_status !== null && (
            <div className="grid gap-1">
              <span className="text-xs font-medium text-muted-foreground">
                {t("agents.approvals.result")} · {t("agents.approvals.resultStatus", { status: a.result_status })}
              </span>
              <Json value={a.result} />
            </div>
          )}
          {pending && (
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="text-xs text-muted-foreground">{t("agents.approvals.expires", { when: formatRelative(a.expires_at) })}</span>
              {canDecide && (
                <div className="flex gap-2">
                  <Button variant="outline" size="sm" onClick={() => onDecide(a, false)}>
                    <X /> {t("agents.approvals.reject")}
                  </Button>
                  <Button size="sm" onClick={() => onDecide(a, true)}>
                    <Check /> {t("agents.approvals.approve")}
                  </Button>
                </div>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export function ApprovalsTab() {
  const t = useT();
  const { canManage } = useRole();
  const approvals = useQuery(approvalsQuery);
  const projects = useQuery(projectsQuery);
  const names = new Map((projects.data ?? []).map((p) => [p.id, p.name]));
  const decide = useDecideApproval();
  const [target, setTarget] = useState<{ a: AgentApproval; approve: boolean } | null>(null);
  const [note, setNote] = useState("");

  if (approvals.isPending) {
    return (
      <div className="grid gap-3">
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-24 w-full" />
      </div>
    );
  }
  if (approvals.isError) return <ErrorState error={approvals.error} onRetry={() => void approvals.refetch()} />;

  const items = approvals.data;
  const pending = items.filter((a) => a.status === "pending");
  const history = items.filter((a) => a.status !== "pending");
  const card = (a: AgentApproval) => (
    <ApprovalCard
      key={a.id}
      a={a}
      projectName={a.project_id ? names.get(a.project_id) : undefined}
      canDecide={canManage}
      onDecide={(x, approve) => {
        setNote("");
        setTarget({ a: x, approve });
      }}
    />
  );

  return (
    <div className="grid gap-6">
      <section className="grid gap-3">
        <h2 className="text-sm font-semibold">{t("agents.approvals.pending")}</h2>
        {pending.length === 0 ? (
          <EmptyState icon={<Inbox />} title={t("agents.approvals.emptyTitle")} description={t("agents.approvals.emptyDesc")} />
        ) : (
          pending.map(card)
        )}
      </section>
      <section className="grid gap-3">
        <h2 className="text-sm font-semibold">{t("agents.approvals.history")}</h2>
        {history.length === 0 ? <p className="text-sm text-muted-foreground">{t("agents.approvals.historyEmpty")}</p> : history.map(card)}
      </section>

      <Dialog open={!!target} onOpenChange={(o) => !o && setTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {target?.approve
                ? t("agents.approvals.approveTitle", { agent: target.a.agent_name ?? target.a.agent_id })
                : t("agents.approvals.rejectTitle")}
            </DialogTitle>
            <DialogDescription>{target?.approve ? t("agents.approvals.approveDesc") : t("agents.approvals.rejectDesc")}</DialogDescription>
          </DialogHeader>
          {target && (
            <div className="truncate rounded-md bg-muted/60 px-2.5 py-2 font-mono text-xs">
              <span className={cn("font-semibold", methodTone(target.a.method))}>{target.a.method}</span> {target.a.path}
            </div>
          )}
          <Textarea
            rows={2}
            value={note}
            placeholder={t("agents.approvals.notePh")}
            aria-label={t("agents.approvals.decidedNote")}
            onChange={(e) => setNote(e.target.value)}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setTarget(null)}>
              {t("agents.approvals.cancel")}
            </Button>
            <Button
              variant={target?.approve ? "default" : "destructive"}
              disabled={decide.isPending}
              onClick={() => {
                if (!target) return;
                decide.mutate(
                  { id: target.a.id, approve: target.approve, note: note.trim() || undefined },
                  { onSettled: () => setTarget(null) },
                );
              }}
            >
              {decide.isPending && <Loader2 className="animate-spin" />}
              {target?.approve ? t("agents.approvals.approve") : t("agents.approvals.reject")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
