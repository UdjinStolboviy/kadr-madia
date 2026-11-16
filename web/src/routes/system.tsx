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

const CAP_INFO: Record<CapabilityKey, { name: string; purpose: string }> = {
  vips: { name: "libvips", purpose: "Image decoding, resizing and encoding" },
  vips_avif: { name: "libvips AVIF", purpose: "AVIF output (via libheif + aom/SVT)" },
  ffmpeg: { name: "ffmpeg", purpose: "Video transcoding and posters" },
  ffprobe: { name: "ffprobe", purpose: "Media inspection and validation" },
  rclone: { name: "rclone", purpose: "Imports from Google Drive, OneDrive, Dropbox" },
  vaapi: { name: "VAAPI", purpose: "Hardware H.264 encoding on Intel iGPU" },
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
        title="System"
        description="Runtime, storage and processing capabilities of this Kadr instance."
        actions={
          <Button variant="outline" size="sm" onClick={() => void system.refetch()} disabled={system.isFetching}>
            <RefreshCw className={system.isFetching ? "animate-spin" : undefined} /> Refresh
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
                  label="Version"
                  value={<span className="font-mono text-lg">{s.version}</span>}
                  sub={
                    <span className="font-mono">
                      {s.commit.slice(0, 10)} · {s.go_version}
                    </span>
                  }
                />
                <Stat icon={<Clock />} label="Uptime" value={formatDuration(s.uptime_sec)} sub={`since ${formatDateTime(s.started_at)}`} />
                <Stat
                  icon={<Database />}
                  label="Database"
                  value={formatBytes(s.db.size_bytes)}
                  sub={`WAL ${formatBytes(s.db.wal_bytes)}`}
                />
                <Stat icon={<FolderOpen />} label="Temp free" value={formatBytes(s.tmp.free_bytes)} sub={s.tmp.path} />
              </>
            )}
          </div>

          <div className="mt-6 grid gap-6 lg:grid-cols-[1fr_22rem]">
            <Card className="gap-0 py-0">
              <CardHeader className="border-b px-5 py-4">
                <CardTitle className="text-base">Capabilities</CardTitle>
                <CardDescription>Detected at startup. Missing tools disable the related pipelines.</CardDescription>
              </CardHeader>
              <CardContent className="px-0">
                <Table>
                  <TableHeader>
                    <TableRow className="hover:bg-transparent">
                      <TableHead className="pl-5">Component</TableHead>
                      <TableHead className="hidden sm:table-cell">Version / detail</TableHead>
                      <TableHead className="pr-5 text-right">Status</TableHead>
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
                              {CAP_INFO[k].name}
                            </div>
                            <div className="text-xs whitespace-normal text-muted-foreground">{CAP_INFO[k].purpose}</div>
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
                              <StatusBadge tone="success">ok</StatusBadge>
                            ) : (
                              <StatusBadge tone={k === "vaapi" || k === "rclone" ? "neutral" : "danger"}>missing</StatusBadge>
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
                  <CardTitle className="text-sm">Runtime</CardTitle>
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
                      <KeyValue label="Mode">
                        {s.dev ? <StatusBadge tone="warning">development</StatusBadge> : <StatusBadge tone="success">production</StatusBadge>}
                      </KeyValue>
                      <KeyValue label="Commit" mono>
                        <span className="inline-flex items-center gap-1">
                          {s.commit.slice(0, 12)}
                          <CopyButton value={s.commit} label="Copy commit" size="icon-xs" />
                        </span>
                      </KeyValue>
                      <KeyValue label="Database" mono>
                        <span title={s.db.path}>{s.db.path}</span>
                      </KeyValue>
                      <KeyValue label="Temp dir" mono>
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
