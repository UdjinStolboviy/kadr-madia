import { useQuery } from "@tanstack/react-query";
import { CheckCircle2, Clock, Database, FolderOpen, RefreshCw, Server, XCircle } from "lucide-react";
import { useEffect, type ReactNode } from "react";

import { CopyButton, KeyValue, PageHeader, StatusBadge } from "@/components/common";
import { SecurityCard } from "@/components/security-card";
import { ErrorState } from "@/components/states";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatBytes, formatDateTime, formatDuration } from "@/lib/format";
import { systemQuery } from "@/lib/queries";
import { CAPABILITY_KEYS, type CapabilityKey } from "@/lib/types";
import { useT } from "@/i18n/react";

const CAP_NAME: Record<CapabilityKey, string> = {
  vips: "libvips",
  vips_avif: "libvips AVIF",
  ffmpeg: "ffmpeg",
  ffprobe: "ffprobe",
  rclone: "rclone",
  vaapi: "VAAPI",
  ghostscript: "Ghostscript",
  qpdf: "qpdf",
};

function Stat({ icon, label, value, sub }: { icon: ReactNode; label: string; value: ReactNode; sub?: ReactNode }) {
  return (
    <Card className="gap-2 py-5">
      <CardContent className="px-5">
        <div className="flex items-center gap-2 text-sm text-muted-foreground [&_svg]:size-4">
          {icon}
          {label}
        </div>
        <div className="mt-2 text-xl font-semibold tracking-tight tabular-nums">{value}</div>
        {sub && <div className="mt-1 truncate text-xs text-muted-foreground">{sub}</div>}
      </CardContent>
    </Card>
  );
}

export default function SystemPage() {
  const t = useT();
  const system = useQuery(systemQuery);
  const s = system.data;

  // Support deep links to #security from the account menu.
  useEffect(() => {
    if (window.location.hash === "#security") {
      document.getElementById("security")?.scrollIntoView({ behavior: "smooth", block: "start" });
    }
  }, [s]);

  return (
    <>
      <PageHeader
        title={t("system.title")}
        description={t("system.description")}
        actions={
          <Button variant="outline" size="sm" onClick={() => void system.refetch()} disabled={system.isFetching}>
            <RefreshCw className={system.isFetching ? "animate-spin" : undefined} /> {t("system.refresh")}
          </Button>
        }
      />

      {system.isError ? (
        <ErrorState className="mb-6" error={system.error} onRetry={() => void system.refetch()} />
      ) : (
        <>
          <div className="grid grid-cols-2 gap-3 sm:gap-4 lg:grid-cols-4">
            {!s ? (
              Array.from({ length: 4 }, (_, i) => <Skeleton key={i} className="h-28 rounded-xl" />)
            ) : (
              <>
                <Stat
                  icon={<Server />}
                  label={t("system.version")}
                  value={<span className="font-mono text-lg">{s.version}</span>}
                  sub={
                    <span className="font-mono">
                      {s.commit.slice(0, 10)} · {s.go_version}
                    </span>
                  }
                />
                <Stat icon={<Clock />} label={t("system.uptime")} value={formatDuration(s.uptime_sec)} sub={t("system.uptimeSince", { date: formatDateTime(s.started_at) })} />
                <Stat
                  icon={<Database />}
                  label={t("system.database")}
                  value={formatBytes(s.db.size_bytes)}
                  sub={t("system.wal", { size: formatBytes(s.db.wal_bytes) })}
                />
                <Stat icon={<FolderOpen />} label={t("system.tempFree")} value={formatBytes(s.tmp.free_bytes)} sub={s.tmp.path} />
              </>
            )}
          </div>

          <div className="mt-6 grid gap-6 lg:grid-cols-[1fr_22rem]">
            <Card className="gap-0 py-0">
              <CardHeader className="border-b px-5 py-4">
                <CardTitle className="text-base">{t("system.capabilities")}</CardTitle>
                <CardDescription>{t("system.capabilitiesDesc")}</CardDescription>
              </CardHeader>
              <CardContent className="px-0">
                <Table>
                  <TableHeader>
                    <TableRow className="hover:bg-transparent">
                      <TableHead className="pl-5">{t("system.col.component")}</TableHead>
                      <TableHead className="hidden sm:table-cell">{t("system.col.detail")}</TableHead>
                      <TableHead className="pr-5 text-right">{t("system.col.status")}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {CAPABILITY_KEYS.map((k) => {
                      const c = s?.capabilities[k];
                      return (
                        <TableRow key={k}>
                          <TableCell className="pl-5">
                            <div className="flex items-center gap-2 font-medium">
                              {!s ? null : c?.ok ? (
                                <CheckCircle2 className="size-4 text-success" />
                              ) : (
                                <XCircle className="size-4 text-muted-foreground" />
                              )}
                              {CAP_NAME[k]}
                            </div>
                            <div className="text-xs whitespace-normal text-muted-foreground">{t(`system.cap.${k}`)}</div>
                            {c?.detail && <div className="mt-0.5 text-xs whitespace-normal text-muted-foreground sm:hidden">{c.detail}</div>}
                          </TableCell>
                          <TableCell className="hidden max-w-[16rem] sm:table-cell">
                            {!s ? (
                              <Skeleton className="h-4 w-24" />
                            ) : (
                              <div className="min-w-0">
                                {c?.version && <div className="truncate font-mono text-xs">{c.version}</div>}
                                {c?.detail && (
                                  <div className="text-xs whitespace-normal text-muted-foreground">{c.detail}</div>
                                )}
                                {!c?.version && !c?.detail && <span className="text-xs text-muted-foreground">—</span>}
                              </div>
                            )}
                          </TableCell>
                          <TableCell className="pr-5 text-right">
                            {!s ? (
                              <Skeleton className="ml-auto h-5 w-14" />
                            ) : c?.ok ? (
                              <StatusBadge tone="success">{t("system.ok")}</StatusBadge>
                            ) : (
                              <StatusBadge tone={k === "vaapi" || k === "rclone" || k === "ghostscript" || k === "qpdf" ? "neutral" : "danger"}>{t("system.missing")}</StatusBadge>
                            )}
                          </TableCell>
                        </TableRow>
                      );
                    })}
                  </TableBody>
                </Table>
              </CardContent>
            </Card>

            <div className="grid h-fit gap-6">
              <Card className="gap-2">
                <CardHeader>
                  <CardTitle className="text-sm">{t("system.runtime")}</CardTitle>
                </CardHeader>
                <CardContent>
                  {!s ? (
                    <div className="space-y-3">
                      <Skeleton className="h-4 w-full" />
                      <Skeleton className="h-4 w-full" />
                      <Skeleton className="h-4 w-2/3" />
                    </div>
                  ) : (
                    <dl className="divide-y">
                      <KeyValue label={t("system.mode")}>
                        {s.dev ? <StatusBadge tone="warning">{t("system.modeDev")}</StatusBadge> : <StatusBadge tone="success">{t("system.modeProd")}</StatusBadge>}
                      </KeyValue>
                      <KeyValue label={t("system.commit")} mono>
                        <span className="inline-flex items-center gap-1">
                          {s.commit.slice(0, 12)}
                          <CopyButton value={s.commit} label={t("system.copyCommit")} size="icon-xs" />
                        </span>
                      </KeyValue>
                      <KeyValue label={t("system.database")} mono>
                        <span title={s.db.path}>{s.db.path}</span>
                      </KeyValue>
                      <KeyValue label={t("system.tempDir")} mono>
                        <span title={s.tmp.path}>{s.tmp.path}</span>
                      </KeyValue>
                    </dl>
                  )}
                </CardContent>
              </Card>
            </div>
          </div>
        </>
      )}

      <div className="mt-6">
        <SecurityCard />
      </div>
    </>
  );
}
