import { useQuery } from "@tanstack/react-query";
import { getRouteApi } from "@tanstack/react-router";
import { Bot, MoreHorizontal, Pencil, Plus, RefreshCw, ShieldOff, Trash2 } from "lucide-react";
import { useState } from "react";

import { ActivityTab } from "@/components/agents/activity-tab";
import { AgentFormSheet } from "@/components/agents/agent-form";
import { APPROVAL_LABEL, LEVEL_LABEL } from "@/components/agents/labels";
import { ApprovalsTab } from "@/components/agents/approvals-tab";
import { SchedulesTab } from "@/components/agents/schedules-tab";
import { AgentTokenDialog } from "@/components/agents/token-dialog";
import { CopyButton, PageHeader, RelativeTime, StatusBadge } from "@/components/common";
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
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { MessageKey } from "@/i18n/dictionaries";
import { useT } from "@/i18n/react";
import { formatDate } from "@/lib/format";
import {
  agentSettingsQuery,
  agentsQuery,
  projectsQuery,
  useRevokeAgent,
  useRotateAgent,
  useSetAgentsEnabled,
} from "@/lib/queries";
import type { Agent, AgentLevel, AgentSecretResponse, AgentStatus } from "@/lib/types";
import { useRole } from "@/lib/use-role";
import { cn } from "@/lib/utils";
import type { AgentsTab } from "@/router";

const route = getRouteApi("/app/agents");

type Tone = "success" | "danger" | "warning" | "neutral" | "info";
const STATUS: Record<AgentStatus, { tone: Tone; label: MessageKey }> = {
  active: { tone: "success", label: "agents.status.active" },
  expired: { tone: "danger", label: "agents.status.expired" },
  revoked: { tone: "neutral", label: "agents.status.revoked" },
};
const LEVEL_TONE: Record<AgentLevel, Tone> = { read: "info", edit: "warning", full: "danger" };

function KillSwitch() {
  const t = useT();
  const { canManage } = useRole();
  const settings = useQuery(agentSettingsQuery);
  const setEnabled = useSetAgentsEnabled();
  const [confirm, setConfirm] = useState(false);
  const enabled = settings.data?.enabled ?? true;
  return (
    <div className={cn("mb-6 rounded-xl border bg-card p-4", !enabled && "border-destructive/40 bg-destructive/5")}>
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-sm font-semibold">
            {!enabled && <ShieldOff className="size-4 text-destructive" />}
            {t("agents.switch.title")}
          </div>
          <p className="mt-0.5 text-sm text-muted-foreground">{enabled ? t("agents.switch.onDesc") : t("agents.switch.offDesc")}</p>
          {settings.data && (
            <div className="mt-2 flex items-center gap-1 text-xs text-muted-foreground">
              {t("agents.mcpEndpoint")}: <code className="font-mono text-foreground">{settings.data.mcp_url}</code>
              <CopyButton value={settings.data.mcp_url} size="icon-xs" />
            </div>
          )}
        </div>
        <label className="flex items-center gap-2 text-sm">
          <span>{t("agents.switch.label")}</span>
          <Switch
            checked={enabled}
            disabled={!canManage || settings.isPending || setEnabled.isPending}
            onCheckedChange={(v) => (v ? setEnabled.mutate(true) : setConfirm(true))}
            aria-label={t("agents.switch.label")}
          />
        </label>
      </div>
      <AlertDialog open={confirm} onOpenChange={setConfirm}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("agents.switch.disableTitle")}</AlertDialogTitle>
            <AlertDialogDescription>{t("agents.switch.disableDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("agents.form.cancel")}</AlertDialogCancel>
            <AlertDialogAction className="bg-destructive text-white hover:bg-destructive/90" onClick={() => setEnabled.mutate(false)}>
              {t("agents.switch.disable")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function AgentsList({ onNew }: { onNew: () => void }) {
  const t = useT();
  const { canManage } = useRole();
  const agents = useQuery(agentsQuery);
  const projects = useQuery(projectsQuery);
  const names = new Map((projects.data ?? []).map((p) => [p.id, p.name]));
  const rotate = useRotateAgent();
  const revoke = useRevokeAgent();
  const [editing, setEditing] = useState<Agent | null>(null);
  const [confirm, setConfirm] = useState<{ agent: Agent; kind: "rotate" | "revoke" } | null>(null);
  const [secret, setSecret] = useState<AgentSecretResponse | null>(null);

  if (agents.isPending) {
    return (
      <div className="space-y-2 rounded-xl border bg-card p-4">
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-12 w-full" />
      </div>
    );
  }
  if (agents.isError) return <ErrorState error={agents.error} onRetry={() => void agents.refetch()} />;
  const list = [...agents.data].sort(
    (a, b) => Number(a.status !== "active") - Number(b.status !== "active") || b.created_at.localeCompare(a.created_at),
  );

  return (
    <>
      {list.length === 0 ? (
        <EmptyState
          icon={<Bot />}
          title={t("agents.emptyTitle")}
          description={t("agents.emptyDesc")}
          action={
            canManage && (
              <Button onClick={onNew}>
                <Plus /> {t("agents.new")}
              </Button>
            )
          }
        />
      ) : (
        <div className="overflow-hidden rounded-xl border bg-card">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="pl-4">{t("agents.col.name")}</TableHead>
                <TableHead>{t("agents.col.level")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("agents.col.projects")}</TableHead>
                <TableHead className="hidden lg:table-cell">{t("agents.col.approval")}</TableHead>
                <TableHead className="hidden sm:table-cell">{t("agents.col.lastUsed")}</TableHead>
                <TableHead className="hidden xl:table-cell">{t("agents.col.expires")}</TableHead>
                <TableHead>{t("agents.col.status")}</TableHead>
                <TableHead className="w-10" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.map((a) => {
                const st = STATUS[a.status];
                return (
                  <TableRow key={a.id} className={cn(a.status !== "active" && "opacity-60")}>
                    <TableCell className="pl-4">
                      <div className="flex items-center gap-1.5 font-medium">
                        {a.name}
                        {a.oauth_client_id && (
                          <span
                            className="rounded bg-brand/10 px-1 py-0.5 text-[10px] font-semibold text-brand"
                            title={t("agents.oauthBadgeTitle")}
                          >
                            {t("agents.oauthBadge")}
                          </span>
                        )}
                      </div>
                      <div className="font-mono text-xs text-muted-foreground">{a.prefix}…</div>
                      {a.description && <div className="max-w-[18rem] truncate text-xs text-muted-foreground">{a.description}</div>}
                    </TableCell>
                    <TableCell>
                      <StatusBadge tone={LEVEL_TONE[a.level]} dot={false}>
                        {t(LEVEL_LABEL[a.level])}
                      </StatusBadge>
                      {a.extra_scopes.length > 0 && (
                        <div className="mt-1 flex flex-wrap gap-1">
                          {a.extra_scopes.map((s) => (
                            <span key={s} className="rounded bg-warning/15 px-1 py-0.5 font-mono text-[10px] text-warning">
                              {s}
                            </span>
                          ))}
                        </div>
                      )}
                    </TableCell>
                    <TableCell className="hidden text-sm md:table-cell">
                      {a.project_ids === null ? (
                        t("agents.allProjects")
                      ) : (
                        <span title={a.project_ids.map((id) => names.get(id) ?? id).join(", ")}>
                          {a.project_ids.length === 1
                            ? (names.get(a.project_ids[0]!) ?? a.project_ids[0])
                            : t("agents.projectsCount", { count: a.project_ids.length })}
                        </span>
                      )}
                    </TableCell>
                    <TableCell className="hidden text-sm lg:table-cell">{t(APPROVAL_LABEL[a.approval_mode])}</TableCell>
                    <TableCell className="hidden text-sm sm:table-cell">
                      <RelativeTime value={a.last_used_at} />
                      {a.last_used_ip && <div className="font-mono text-[11px] text-muted-foreground">{a.last_used_ip}</div>}
                    </TableCell>
                    <TableCell className="hidden text-sm text-muted-foreground xl:table-cell">
                      {a.expires_at ? formatDate(a.expires_at) : t("agents.never")}
                    </TableCell>
                    <TableCell>
                      <StatusBadge tone={st.tone}>{t(st.label)}</StatusBadge>
                    </TableCell>
                    <TableCell className="pr-3">
                      {canManage && a.status !== "revoked" && (
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <Button variant="ghost" size="icon-sm" aria-label={t("agents.menu", { name: a.name })}>
                              <MoreHorizontal />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem onSelect={() => setEditing(a)}>
                              <Pencil /> {t("agents.edit")}
                            </DropdownMenuItem>
                            <DropdownMenuItem onSelect={() => setConfirm({ agent: a, kind: "rotate" })}>
                              <RefreshCw /> {t("agents.rotate")}
                            </DropdownMenuItem>
                            <DropdownMenuSeparator />
                            <DropdownMenuItem variant="destructive" onSelect={() => setConfirm({ agent: a, kind: "revoke" })}>
                              <Trash2 /> {t("agents.revoke")}
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      )}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
      )}

      <AgentFormSheet
        open={!!editing}
        agent={editing ?? undefined}
        onOpenChange={(o) => !o && setEditing(null)}
        onCreated={setSecret}
      />
      <AgentTokenDialog result={secret} onDone={() => setSecret(null)} />

      <AlertDialog open={!!confirm} onOpenChange={(o) => !o && setConfirm(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {confirm?.kind === "rotate"
                ? t("agents.rotateTitle", { name: confirm.agent.name })
                : t("agents.revokeTitle", { name: confirm?.agent.name ?? "" })}
            </AlertDialogTitle>
            <AlertDialogDescription>{confirm?.kind === "rotate" ? t("agents.rotateDesc") : t("agents.revokeDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("agents.form.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className={cn(confirm?.kind === "revoke" && "bg-destructive text-white hover:bg-destructive/90")}
              onClick={() => {
                if (!confirm) return;
                if (confirm.kind === "rotate") rotate.mutate(confirm.agent.id, { onSuccess: setSecret });
                else revoke.mutate(confirm.agent.id);
                setConfirm(null);
              }}
            >
              {confirm?.kind === "rotate" ? t("agents.rotate") : t("agents.revoke")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

export default function AgentsPage() {
  const t = useT();
  const { tab = "agents" } = route.useSearch();
  const navigate = route.useNavigate();
  const { canManage } = useRole();
  const settings = useQuery(agentSettingsQuery);
  const [creating, setCreating] = useState(false);
  const [secret, setSecret] = useState<AgentSecretResponse | null>(null);
  const pending = settings.data?.pending_approvals ?? 0;

  return (
    <>
      <PageHeader
        title={t("agents.title")}
        description={t("agents.description")}
        actions={
          canManage && (
            <Button onClick={() => setCreating(true)}>
              <Plus /> {t("agents.new")}
            </Button>
          )
        }
      />
      {settings.data && !settings.data.enabled && (
        <p className="mb-4 flex items-center gap-2 rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive" role="alert">
          <ShieldOff className="size-4 shrink-0" />
          {t("agents.switch.offBanner")}
        </p>
      )}
      {!canManage && <p className="mb-4 rounded-lg border bg-muted/40 px-3 py-2 text-sm text-muted-foreground">{t("agents.viewerNote")}</p>}
      <KillSwitch />

      <Tabs value={tab} onValueChange={(v) => void navigate({ search: { tab: v as AgentsTab }, replace: true })}>
        <TabsList variant="line" className="mb-4 w-full justify-start overflow-x-auto border-b pb-0">
          <TabsTrigger className="flex-none px-3" value="agents">
            {t("agents.tab.agents")}
          </TabsTrigger>
          <TabsTrigger className="flex-none gap-1.5 px-3" value="approvals">
            {t("agents.tab.approvals")}
            {pending > 0 && (
              <span className="rounded-full bg-warning px-1.5 text-[11px] font-semibold text-white tabular-nums">{pending}</span>
            )}
          </TabsTrigger>
          <TabsTrigger className="flex-none px-3" value="schedules">
            {t("agents.tab.schedules")}
          </TabsTrigger>
          <TabsTrigger className="flex-none px-3" value="activity">
            {t("agents.tab.activity")}
          </TabsTrigger>
        </TabsList>
        <TabsContent value="agents">
          <AgentsList onNew={() => setCreating(true)} />
        </TabsContent>
        <TabsContent value="approvals">
          <ApprovalsTab />
        </TabsContent>
        <TabsContent value="schedules">
          <SchedulesTab />
        </TabsContent>
        <TabsContent value="activity">
          <ActivityTab />
        </TabsContent>
      </Tabs>

      <AgentFormSheet open={creating} onOpenChange={setCreating} onCreated={setSecret} />
      <AgentTokenDialog result={secret} onDone={() => setSecret(null)} />
    </>
  );
}
