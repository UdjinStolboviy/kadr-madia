import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronDown, Loader2, ScrollText } from "lucide-react";
import { Fragment, useState } from "react";

import { PageHeader, RelativeTime } from "@/components/common";
import { EmptyState, ErrorState } from "@/components/states";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import { projectsQuery, qk } from "@/lib/queries";
import type { AuditEntry } from "@/lib/types";
import { cn } from "@/lib/utils";
import { useT } from "@/i18n/react";

const PAGE = 50;

function actionTone(action: string) {
  if (/(delete|revoke|disable|fail)/.test(action)) return "text-destructive";
  if (/(create|enable)/.test(action)) return "text-success";
  if (/(login|logout|auth)/.test(action)) return "text-brand";
  return "text-foreground";
}

function hasDetails(d: unknown) {
  if (d === null || d === undefined || d === "") return false;
  if (typeof d === "object" && Object.keys(d as object).length === 0) return false;
  return true;
}

function Row({ e, projectName }: { e: AuditEntry; projectName?: string }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const expandable = hasDetails(e.details);
  return (
    <Fragment>
      <TableRow
        className={cn(expandable && "cursor-pointer")}
        onClick={() => expandable && setOpen((o) => !o)}
        aria-expanded={expandable ? open : undefined}
      >
        <TableCell className="pl-4 text-sm text-muted-foreground">
          <RelativeTime value={e.created_at} />
        </TableCell>
        <TableCell>
          <span className={cn("font-mono text-xs font-medium", actionTone(e.action))}>{e.action}</span>
          <div className="mt-0.5 truncate text-xs text-muted-foreground md:hidden">{e.target}</div>
        </TableCell>
        <TableCell className="hidden max-w-[16rem] md:table-cell">
          <span className="block truncate font-mono text-xs" title={e.target ?? undefined}>
            {e.target ?? "—"}
          </span>
        </TableCell>
        <TableCell className="hidden text-sm lg:table-cell">
          {e.project_id ? (
            <Link
              to="/projects/$projectId"
              params={{ projectId: e.project_id }}
              className="hover:underline"
              onClick={(ev) => ev.stopPropagation()}
            >
              {projectName ?? e.project_id}
            </Link>
          ) : (
            <span className="text-muted-foreground">—</span>
          )}
        </TableCell>
        <TableCell className="hidden sm:table-cell">
          <div className="text-xs">
            <span className="capitalize">{e.actor_type}</span>
            {e.actor_id && <span className="ml-1 font-mono text-muted-foreground">{e.actor_id.slice(0, 10)}</span>}
          </div>
        </TableCell>
        <TableCell className="hidden font-mono text-xs text-muted-foreground xl:table-cell">{e.ip ?? "—"}</TableCell>
        <TableCell className="w-8 pr-4">
          {expandable && (
            <button
              type="button"
              className="rounded-sm text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
              aria-label={open ? t("audit.hideDetails") : t("audit.showDetails")}
              onClick={(ev) => {
                ev.stopPropagation();
                setOpen((o) => !o);
              }}
            >
              <ChevronDown className={cn("size-4 transition-transform", open && "rotate-180")} />
            </button>
          )}
        </TableCell>
      </TableRow>
      {open && (
        <TableRow className="bg-muted/30 hover:bg-muted/30">
          <TableCell colSpan={7} className="px-4">
            <pre className="max-h-64 overflow-auto font-mono text-xs leading-relaxed whitespace-pre-wrap">
              {JSON.stringify(e.details, null, 2)}
            </pre>
          </TableCell>
        </TableRow>
      )}
    </Fragment>
  );
}

export default function AuditPage() {
  const t = useT();
  const projects = useQuery(projectsQuery);
  const names = new Map((projects.data ?? []).map((p) => [p.id, p.name]));
  const audit = useInfiniteQuery({
    queryKey: qk.audit,
    queryFn: ({ pageParam }) => api.audit.list({ limit: PAGE, cursor: pageParam }),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.next_cursor || null,
  });

  const items = audit.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <>
      <PageHeader title={t("audit.title")} description={t("audit.description")} />
      {audit.isPending ? (
        <div className="space-y-2 rounded-xl border bg-card p-4">
          {Array.from({ length: 8 }, (_, i) => (
            <Skeleton key={i} className="h-9 w-full" />
          ))}
        </div>
      ) : audit.isError ? (
        <ErrorState error={audit.error} onRetry={() => void audit.refetch()} />
      ) : items.length === 0 ? (
        <EmptyState icon={<ScrollText />} title={t("audit.emptyTitle")} description={t("audit.emptyDesc")} />
      ) : (
        <>
          <div className="overflow-hidden rounded-xl border bg-card">
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="w-32 pl-4">{t("audit.col.when")}</TableHead>
                  <TableHead>{t("audit.col.action")}</TableHead>
                  <TableHead className="hidden md:table-cell">{t("audit.col.target")}</TableHead>
                  <TableHead className="hidden lg:table-cell">{t("audit.col.project")}</TableHead>
                  <TableHead className="hidden sm:table-cell">{t("audit.col.actor")}</TableHead>
                  <TableHead className="hidden xl:table-cell">{t("audit.col.ip")}</TableHead>
                  <TableHead className="w-8" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((e) => (
                  <Row key={e.id} e={e} projectName={e.project_id ? names.get(e.project_id) : undefined} />
                ))}
              </TableBody>
            </Table>
          </div>
          <div className="mt-4 flex items-center justify-between text-sm text-muted-foreground">
            <span>
              {t("audit.count", { count: items.length })}
            </span>
            {audit.hasNextPage ? (
              <Button variant="outline" size="sm" onClick={() => void audit.fetchNextPage()} disabled={audit.isFetchingNextPage}>
                {audit.isFetchingNextPage && <Loader2 className="animate-spin" />}
                {t("audit.loadMore")}
              </Button>
            ) : (
              <span>{t("audit.end")}</span>
            )}
          </div>
        </>
      )}
    </>
  );
}
