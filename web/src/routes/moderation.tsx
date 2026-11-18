import { useInfiniteQuery } from "@tanstack/react-query";
import { getRouteApi } from "@tanstack/react-router";
import { Bot, Check, Flag, FolderKanban, ImageOff, Loader2, ShieldCheck, X } from "lucide-react";
import { useCallback, useState } from "react";
import { toast } from "sonner";

import { Field, PageHeader, RelativeTime, StatusBadge } from "@/components/common";
import { FilterSelect, ProjectSelect } from "@/components/project-select";
import { EmptyState, ErrorState } from "@/components/states";
import { TagInput } from "@/components/tag-input";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import type { MessageKey } from "@/i18n/dictionaries";
import { useT } from "@/i18n/react";
import { api } from "@/lib/api";
import { qk, useSetModeration } from "@/lib/queries";
import { ASSET_KINDS, type ModerationDecision, type ModerationItem, type ModerationStatus } from "@/lib/types";
import { useActiveProject } from "@/lib/use-active-project";
import { useRole } from "@/lib/use-role";
import { cn } from "@/lib/utils";

const route = getRouteApi("/app/moderation");
const PAGE = 24;
const FILTERS = ["unreviewed", "flagged", "approved", "rejected", "all"] as const;
type Filter = (typeof FILTERS)[number];

type Tone = "success" | "danger" | "warning" | "neutral" | "info";
const STATUS: Record<ModerationStatus, { tone: Tone; label: MessageKey }> = {
  unreviewed: { tone: "neutral", label: "moderation.status.unreviewed" },
  approved: { tone: "success", label: "moderation.status.approved" },
  flagged: { tone: "warning", label: "moderation.status.flagged" },
  rejected: { tone: "danger", label: "moderation.status.rejected" },
};
const DECISION_LABEL: Record<ModerationDecision, MessageKey> = {
  approved: "moderation.approve",
  flagged: "moderation.flag",
  rejected: "moderation.reject",
};
const LABEL_RE = /^[a-z0-9][a-z0-9_.:-]{0,39}$/;

function Preview({ projectId, item }: { projectId: string; item: ModerationItem }) {
  const t = useT();
  const [failed, setFailed] = useState(false);
  return (
    <div className="flex aspect-square items-center justify-center overflow-hidden rounded-t-xl bg-muted">
      {failed ? (
        <div className="flex flex-col items-center gap-1 text-xs text-muted-foreground">
          <ImageOff className="size-5 opacity-60" />
          {t("moderation.noPreview")}
        </div>
      ) : (
        <img
          src={api.moderation.previewUrl(projectId, item.asset_id, 480)}
          alt=""
          loading="lazy"
          className="size-full object-cover"
          onError={() => setFailed(true)}
        />
      )}
    </div>
  );
}

export default function ModerationPage() {
  const t = useT();
  const { canManage } = useRole();
  const { project, status: statusParam } = route.useSearch();
  const navigate = route.useNavigate();
  const status: Filter = statusParam ?? "unreviewed";
  const setProject = useCallback(
    (id: string) => void navigate({ search: (s) => ({ ...s, project: id }), replace: true }),
    [navigate],
  );
  const { projects, list, active } = useActiveProject(project, setProject);
  const [kind, setKind] = useState<string | undefined>();
  const [decide, setDecide] = useState<{ item: ModerationItem; status: ModerationDecision } | null>(null);
  const [labels, setLabels] = useState<string[]>([]);
  const [note, setNote] = useState("");

  const pid = active?.id ?? "";
  const setModeration = useSetModeration(pid);
  const queue = useInfiniteQuery({
    queryKey: [...qk.moderation(pid), { status, kind }],
    queryFn: ({ pageParam }) => api.moderation.queue(pid, { status, kind, cursor: pageParam, limit: PAGE }),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.next_cursor || null,
    enabled: !!pid,
  });
  const items = queue.data?.pages.flatMap((p) => p.items) ?? [];

  function open(item: ModerationItem, s: ModerationDecision) {
    setLabels(item.moderation.labels);
    setNote(item.moderation.note ?? "");
    setDecide({ item, status: s });
  }

  return (
    <>
      <PageHeader
        title={t("moderation.title")}
        description={t("moderation.description")}
        actions={<ProjectSelect projects={list} value={active?.id} onChange={setProject} loading={projects.isPending} />}
      />
      {projects.isError ? (
        <ErrorState error={projects.error} onRetry={() => void projects.refetch()} />
      ) : projects.isSuccess && list.length === 0 ? (
        <EmptyState icon={<FolderKanban />} title={t("moderation.noProjectsTitle")} description={t("moderation.noProjectsDesc")} />
      ) : (
        <>
          <div className="mb-4 flex flex-wrap items-center gap-2">
            <div className="flex flex-wrap gap-1 rounded-lg border p-0.5" role="tablist" aria-label={t("moderation.status")}>
              {FILTERS.map((f) => (
                <button
                  key={f}
                  type="button"
                  role="tab"
                  aria-selected={status === f}
                  onClick={() => void navigate({ search: (s) => ({ ...s, status: f }), replace: true })}
                  className={cn(
                    "rounded-md px-2.5 py-1 text-sm text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50",
                    status === f && "bg-accent font-medium text-foreground",
                  )}
                >
                  {f === "all" ? t("moderation.all") : t(STATUS[f].label)}
                </button>
              ))}
            </div>
            <FilterSelect value={kind} onChange={setKind} options={ASSET_KINDS} allLabel={t("moderation.allKinds")} label={t("moderation.kind")} />
          </div>

          {queue.isPending ? (
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
              {Array.from({ length: 8 }, (_, i) => (
                <Skeleton key={i} className="aspect-[3/4] w-full rounded-xl" />
              ))}
            </div>
          ) : queue.isError ? (
            <ErrorState error={queue.error} onRetry={() => void queue.refetch()} />
          ) : items.length === 0 ? (
            <EmptyState icon={<ShieldCheck />} title={t("moderation.emptyTitle")} description={t("moderation.emptyDesc")} />
          ) : (
            <>
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
                {items.map((it) => {
                  const st = STATUS[it.moderation.status];
                  return (
                    <div key={it.asset_id} className="flex flex-col rounded-xl border bg-card">
                      <Preview projectId={pid} item={it} />
                      <div className="grid flex-1 gap-1.5 p-2.5">
                        <div className="flex items-center justify-between gap-1">
                          <StatusBadge tone={st.tone}>{t(st.label)}</StatusBadge>
                          {it.moderation.actor_type === "agent" && (
                            <span className="inline-flex items-center gap-0.5 text-[11px] text-brand" title={t("moderation.byAgent")}>
                              <Bot className="size-3" />
                            </span>
                          )}
                        </div>
                        <div className="truncate font-mono text-[11px] text-muted-foreground" title={it.asset_id}>
                          {it.asset_id}
                        </div>
                        <div className="truncate text-xs text-muted-foreground">
                          {it.namespace} · <RelativeTime value={it.created_at} />
                        </div>
                        {it.moderation.labels.length > 0 && (
                          <div className="flex flex-wrap gap-1">
                            {it.moderation.labels.map((l) => (
                              <span key={l} className="rounded bg-secondary px-1 py-0.5 font-mono text-[10px]">
                                {l}
                              </span>
                            ))}
                          </div>
                        )}
                        {it.moderation.note && <p className="line-clamp-2 text-xs text-muted-foreground">{it.moderation.note}</p>}
                        {canManage && (
                          <div className="mt-auto flex gap-1 pt-1">
                            <Button
                              size="icon-sm"
                              variant="outline"
                              className="flex-1 text-success"
                              aria-label={t("moderation.approve")}
                              onClick={() =>
                                setModeration.mutate(
                                  { assetId: it.asset_id, body: { status: "approved", labels: [] } },
                                  { onSuccess: () => toast.success(t("moderation.toast.saved")) },
                                )
                              }
                            >
                              <Check />
                            </Button>
                            <Button size="icon-sm" variant="outline" className="flex-1 text-warning" aria-label={t("moderation.flag")} onClick={() => open(it, "flagged")}>
                              <Flag />
                            </Button>
                            <Button
                              size="icon-sm"
                              variant="outline"
                              className="flex-1 text-destructive"
                              aria-label={t("moderation.reject")}
                              onClick={() => open(it, "rejected")}
                            >
                              <X />
                            </Button>
                          </div>
                        )}
                      </div>
                    </div>
                  );
                })}
              </div>
              <div className="mt-4 flex justify-end text-sm text-muted-foreground">
                {queue.hasNextPage ? (
                  <Button variant="outline" size="sm" onClick={() => void queue.fetchNextPage()} disabled={queue.isFetchingNextPage}>
                    {queue.isFetchingNextPage && <Loader2 className="animate-spin" />}
                    {t("moderation.loadMore")}
                  </Button>
                ) : (
                  <span>{t("moderation.end")}</span>
                )}
              </div>
            </>
          )}
        </>
      )}

      <Dialog open={!!decide} onOpenChange={(o) => !o && setDecide(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {decide && t("moderation.decideTitle", { decision: t(DECISION_LABEL[decide.status]), id: decide.item.asset_id })}
            </DialogTitle>
            <DialogDescription>{t("moderation.description")}</DialogDescription>
          </DialogHeader>
          <Field label={t("moderation.labels")} htmlFor="mod-labels">
            <TagInput
              id="mod-labels"
              value={labels}
              onChange={setLabels}
              placeholder={t("moderation.labelsPh")}
              validate={(v) => (LABEL_RE.test(v) ? null : t("moderation.labelInvalid"))}
            />
          </Field>
          <Field label={t("moderation.note")} htmlFor="mod-note">
            <Textarea id="mod-note" rows={2} value={note} placeholder={t("moderation.notePh")} onChange={(e) => setNote(e.target.value)} />
          </Field>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDecide(null)}>
              {t("moderation.cancel")}
            </Button>
            <Button
              variant={decide?.status === "rejected" ? "destructive" : "default"}
              disabled={setModeration.isPending}
              onClick={() => {
                if (!decide) return;
                setModeration.mutate(
                  { assetId: decide.item.asset_id, body: { status: decide.status, labels, note: note.trim() || undefined } },
                  {
                    onSuccess: () => {
                      toast.success(t("moderation.toast.saved"));
                      setDecide(null);
                    },
                  },
                );
              }}
            >
              {setModeration.isPending && <Loader2 className="animate-spin" />}
              {t("moderation.save")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
