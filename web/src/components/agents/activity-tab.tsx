import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Bot, Loader2 } from "lucide-react";
import { useState } from "react";

import { AuditRow } from "@/components/audit-row";
import { EmptyState, ErrorState } from "@/components/states";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useT } from "@/i18n/react";
import { api } from "@/lib/api";
import { agentsQuery, projectsQuery, qk } from "@/lib/queries";

const ALL = "__all";
const PAGE = 50;

/** Audit log entries written by agents (changes, refusals, approval requests). */
export function ActivityTab() {
  const t = useT();
  const agents = useQuery(agentsQuery);
  const projects = useQuery(projectsQuery);
  const [agentId, setAgentId] = useState<string>(ALL);
  const actor = agentId === ALL ? undefined : agentId;
  const names = new Map((projects.data ?? []).map((p) => [p.id, p.name]));
  const agentNames = new Map((agents.data ?? []).map((a) => [a.id, a.name]));
  const audit = useInfiniteQuery({
    queryKey: [...qk.audit, "agent", actor ?? ""],
    queryFn: ({ pageParam }) => api.audit.list({ limit: PAGE, cursor: pageParam, actor_type: "agent", actor_id: actor }),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.next_cursor || null,
  });
  const items = audit.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <div className="grid gap-4">
      <div className="flex items-center gap-2">
        <Select value={agentId} onValueChange={setAgentId}>
          <SelectTrigger size="sm" className="w-56" aria-label={t("agents.activity.filter")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t("agents.activity.allAgents")}</SelectItem>
            {(agents.data ?? []).map((a) => (
              <SelectItem key={a.id} value={a.id}>
                {a.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {audit.isPending ? (
        <div className="space-y-2 rounded-xl border bg-card p-4">
          {Array.from({ length: 6 }, (_, i) => (
            <Skeleton key={i} className="h-9 w-full" />
          ))}
        </div>
      ) : audit.isError ? (
        <ErrorState error={audit.error} onRetry={() => void audit.refetch()} />
      ) : items.length === 0 ? (
        <EmptyState icon={<Bot />} title={t("agents.activity.emptyTitle")} description={t("agents.activity.emptyDesc")} />
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
                    agentName={e.actor_id ? agentNames.get(e.actor_id) : undefined}
                  />
                ))}
              </TableBody>
            </Table>
          </div>
          <div className="flex justify-end text-sm text-muted-foreground">
            {audit.hasNextPage ? (
              <Button variant="outline" size="sm" onClick={() => void audit.fetchNextPage()} disabled={audit.isFetchingNextPage}>
                {audit.isFetchingNextPage && <Loader2 className="animate-spin" />}
                {t("agents.activity.loadMore")}
              </Button>
            ) : (
              <span>{t("agents.activity.end")}</span>
            )}
          </div>
        </>
      )}
    </div>
  );
}
