import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  ArrowRight,
  Check,
  CheckCircle2,
  Cpu,
  FolderKanban,
  HardDrive,
  KeyRound,
  XCircle,
  type LucideIcon,
} from "lucide-react";
import type { ReactNode } from "react";

import { PageHeader, RelativeTime, StatusBadge, TestStatusBadge } from "@/components/common";
import { useT } from "@/i18n/react";
import { ErrorState } from "@/components/states";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { formatBytes, formatDuration } from "@/lib/format";
import { meQuery, projectsQuery, storageQuery, systemQuery } from "@/lib/queries";
import { CAPABILITY_KEYS } from "@/lib/types";
import { cn } from "@/lib/utils";

const CAP_LABEL: Record<string, string> = {
  vips: "libvips",
  vips_avif: "AVIF",
  ffmpeg: "ffmpeg",
  ffprobe: "ffprobe",
  rclone: "rclone",
  vaapi: "VAAPI",
};

function StatCard({
  title,
  icon: Icon,
  to,
  loading,
  value,
  footer,
}: {
  title: string;
  icon: LucideIcon;
  to: "/projects" | "/storage" | "/system";
  loading: boolean;
  value: ReactNode;
  footer: ReactNode;
}) {
  const t = useT();
  return (
    <Card className="group relative gap-3 py-5 transition-colors hover:border-foreground/15">
      <CardHeader className="flex flex-row items-center justify-between px-5">
        <CardTitle className="text-sm font-medium text-muted-foreground">{title}</CardTitle>
        <Icon className="size-4 text-muted-foreground" />
      </CardHeader>
      <CardContent className="px-5">
        {loading ? (
          <>
            <Skeleton className="h-8 w-16" />
            <Skeleton className="mt-3 h-4 w-40" />
          </>
        ) : (
          <>
            <div className="text-2xl font-semibold tracking-tight tabular-nums">{value}</div>
            <div className="mt-2 text-sm text-muted-foreground">{footer}</div>
          </>
        )}
      </CardContent>
      <Link to={to} className="absolute inset-0 rounded-xl outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
        <span className="sr-only">{t("overview.open", { title })}</span>
      </Link>
    </Card>
  );
}

function SetupStep({
  n,
  title,
  description,
  done,
  active,
  action,
}: {
  n: number;
  title: string;
  description: string;
  done: boolean;
  active: boolean;
  action?: ReactNode;
}) {
  return (
    <li
      className={cn(
        "flex flex-1 flex-col gap-3 rounded-lg border p-4 transition-colors",
        active ? "border-brand/40 bg-brand/5" : "bg-card",
      )}
    >
      <div className="flex items-center gap-2.5">
        <span
          className={cn(
            "flex size-6 shrink-0 items-center justify-center rounded-full text-xs font-semibold",
            done ? "bg-success text-white" : active ? "bg-brand text-white" : "bg-muted text-muted-foreground",
          )}
        >
          {done ? <Check className="size-3.5" /> : n}
        </span>
        <span className={cn("text-sm font-medium", done && "text-muted-foreground line-through")}>{title}</span>
      </div>
      <p className="text-sm text-muted-foreground">{description}</p>
      {active && action && <div className="mt-auto">{action}</div>}
    </li>
  );
}

function GettingStarted({ hasStorage, hasProject, firstProjectId }: { hasStorage: boolean; hasProject: boolean; firstProjectId?: string }) {
  const t = useT();
  const current = !hasStorage ? 1 : !hasProject ? 2 : 3;
  return (
    <Card className="mb-6 gap-4 py-5">
      <CardHeader className="px-5">
        <CardTitle className="text-base">{t("overview.start.title")}</CardTitle>
        <p className="text-sm text-muted-foreground">{t("overview.start.subtitle")}</p>
      </CardHeader>
      <CardContent className="px-5">
        <ol className="flex flex-col gap-3 md:flex-row">
          <SetupStep
            n={1}
            title={t("overview.start.s1.title")}
            description={t("overview.start.s1.desc")}
            done={hasStorage}
            active={current === 1}
            action={
              <Button asChild size="sm">
                <Link to="/storage" search={{ new: true }}>
                  <HardDrive /> {t("overview.start.s1.action")}
                </Link>
              </Button>
            }
          />
          <SetupStep
            n={2}
            title={t("overview.start.s2.title")}
            description={t("overview.start.s2.desc")}
            done={hasProject}
            active={current === 2}
            action={
              <Button asChild size="sm">
                <Link to="/projects" search={{ new: true }}>
                  <FolderKanban /> {t("overview.start.s2.action")}
                </Link>
              </Button>
            }
          />
          <SetupStep
            n={3}
            title={t("overview.start.s3.title")}
            description={t("overview.start.s3.desc")}
            done={false}
            active={current === 3}
            action={
              firstProjectId ? (
                <Button asChild size="sm">
                  <Link to="/projects/$projectId" params={{ projectId: firstProjectId }} search={{ tab: "keys" }}>
                    <KeyRound /> {t("overview.start.s3.action")}
                  </Link>
                </Button>
              ) : undefined
            }
          />
        </ol>
      </CardContent>
    </Card>
  );
}

export default function OverviewPage() {
  const t = useT();
  const me = useQuery(meQuery);
  const projects = useQuery(projectsQuery);
  const storage = useQuery(storageQuery);
  const system = useQuery(systemQuery);

  const projectList = projects.data ?? [];
  const storageList = storage.data ?? [];
  const active = projectList.filter((p) => !p.disabled).length;
  const used = projectList.reduce((s, p) => s + (p.used_bytes || 0), 0);
  const passed = storageList.filter((s) => s.last_test_ok === true).length;
  const failed = storageList.filter((s) => s.last_test_ok === false).length;
  const caps = system.data ? CAPABILITY_KEYS.map((k) => [k, system.data.capabilities[k]] as const) : [];
  const capsOk = caps.filter(([, c]) => c?.ok).length;

  const loaded = projects.isSuccess && storage.isSuccess;
  const showSetup = loaded && (storageList.length === 0 || projectList.length === 0);
  const hasKeysHint = loaded && !showSetup;

  const firstName = me.data?.admin.email.split("@")[0];

  return (
    <>
      <PageHeader
        title={firstName ? t("overview.welcome", { name: firstName }) : t("overview.title")}
        description={t("overview.description")}
      />

      {(projects.isError || storage.isError) && (
        <ErrorState
          className="mb-6"
          error={projects.error ?? storage.error}
          onRetry={() => {
            void projects.refetch();
            void storage.refetch();
          }}
        />
      )}

      {showSetup && (
        <GettingStarted
          hasStorage={storageList.length > 0}
          hasProject={projectList.length > 0}
          firstProjectId={projectList[0]?.id}
        />
      )}

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <StatCard
          title={t("overview.projects")}
          icon={FolderKanban}
          to="/projects"
          loading={projects.isPending}
          value={projectList.length}
          footer={
            projectList.length ? (
              t("overview.projects.footer", { active, size: formatBytes(used) })
            ) : (
              t("overview.projects.none")
            )
          }
        />
        <StatCard
          title={t("overview.storage")}
          icon={HardDrive}
          to="/storage"
          loading={storage.isPending}
          value={storageList.length}
          footer={
            storageList.length === 0 ? (
              t("overview.storage.empty")
            ) : (
              <span className="flex flex-wrap items-center gap-1.5">
                {passed > 0 && <StatusBadge tone="success">{t("overview.storage.passing", { n: passed })}</StatusBadge>}
                {failed > 0 && <StatusBadge tone="danger">{t("overview.storage.failing", { n: failed })}</StatusBadge>}
                {storageList.length - passed - failed > 0 && (
                  <StatusBadge tone="neutral">{t("overview.storage.untested", { n: storageList.length - passed - failed })}</StatusBadge>
                )}
              </span>
            )
          }
        />
        <StatCard
          title={t("overview.system")}
          icon={Cpu}
          to="/system"
          loading={system.isPending}
          value={
            system.data ? (
              <span>
                {capsOk}
                <span className="text-muted-foreground">/{caps.length}</span>
              </span>
            ) : (
              "—"
            )
          }
          footer={
            system.isError
              ? t("overview.system.unavailable")
              : system.data
                ? t("overview.system.footer", { up: formatDuration(system.data.uptime_sec) })
                : null
          }
        />
      </div>

      <div className="mt-6 grid gap-4 lg:grid-cols-2">
        <Card className="gap-0 py-0">
          <CardHeader className="flex flex-row items-center justify-between border-b px-5 py-4">
            <CardTitle className="text-sm">{t("overview.health.title")}</CardTitle>
            <Button asChild variant="ghost" size="xs">
              <Link to="/storage">
                {t("overview.manage")} <ArrowRight />
              </Link>
            </Button>
          </CardHeader>
          <CardContent className="px-0">
            {storage.isPending ? (
              <div className="space-y-3 p-5">
                <Skeleton className="h-5 w-full" />
                <Skeleton className="h-5 w-3/4" />
              </div>
            ) : storageList.length === 0 ? (
              <p className="p-5 text-sm text-muted-foreground">{t("overview.health.empty")}</p>
            ) : (
              <ul className="divide-y">
                {storageList.map((s) => (
                  <li key={s.id} className="flex items-center justify-between gap-3 px-5 py-3">
                    <div className="min-w-0">
                      <div className="truncate text-sm font-medium">{s.name}</div>
                      <div className="text-xs text-muted-foreground">
                        <span className="uppercase">{s.provider}</span> · {t("overview.health.tested")} <RelativeTime value={s.last_test_at} />
                      </div>
                    </div>
                    <TestStatusBadge ok={s.last_test_ok} at={s.last_test_at} />
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>

        <Card className="gap-0 py-0">
          <CardHeader className="flex flex-row items-center justify-between border-b px-5 py-4">
            <CardTitle className="text-sm">{t("overview.caps.title")}</CardTitle>
            <Button asChild variant="ghost" size="xs">
              <Link to="/system">
                {t("overview.details")} <ArrowRight />
              </Link>
            </Button>
          </CardHeader>
          <CardContent className="p-5">
            {system.isPending ? (
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
                {Array.from({ length: 6 }, (_, i) => (
                  <Skeleton key={i} className="h-9" />
                ))}
              </div>
            ) : system.isError ? (
              <p className="text-sm text-muted-foreground">{t("overview.caps.error")}</p>
            ) : (
              <ul className="grid grid-cols-2 gap-2 sm:grid-cols-3">
                {caps.map(([k, c]) => (
                  <li key={k} className="flex items-center gap-2 rounded-md border px-3 py-2 text-sm">
                    {c?.ok ? (
                      <CheckCircle2 className="size-4 shrink-0 text-success" />
                    ) : (
                      <XCircle className="size-4 shrink-0 text-muted-foreground" />
                    )}
                    <span className={cn("truncate", !c?.ok && "text-muted-foreground")}>{CAP_LABEL[k] ?? k}</span>
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
      </div>

      {hasKeysHint && (
        <p className="mt-6 text-center text-xs text-muted-foreground">
          {t("overview.tip")}
        </p>
      )}
    </>
  );
}
