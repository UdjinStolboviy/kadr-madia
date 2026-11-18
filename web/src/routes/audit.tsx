import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Loader2, ScrollText } from "lucide-react";

import { AuditRow } from "@/components/audit-row";
import { PageHeader } from "@/components/common";
import { EmptyState, ErrorState } from "@/components/states";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import { agentsQuery, projectsQuery, qk } from "@/lib/queries";
import { useT } from "@/i18n/react";

const PAGE = 50;

export default function AuditPage() {
  const t = useT();
  const projects = useQuery(projectsQuery);
  const names = new Map((projects.data ?? []).map((p) => [p.id, p.name]));
  const agents = useQuery(agentsQuery);
  const agentNames = new Map((agents.data ?? []).map((a) => [a.id, a.name]));
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
                  <AuditRow
                    key={e.id}
                    e={e}
                    projectName={e.project_id ? names.get(e.project_id) : undefined}
                    agentName={e.actor_type === "agent" && e.actor_id ? agentNames.get(e.actor_id) : undefined}
                  />
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
