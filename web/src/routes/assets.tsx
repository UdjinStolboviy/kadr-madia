import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { getRouteApi } from "@tanstack/react-router";
import { Download, FileText, FolderKanban, ImageOff, Images, Loader2, Trash2, Video } from "lucide-react";
import { useCallback, useState } from "react";

import { CopyButton, KeyValue, PageHeader, RelativeTime, StatusBadge } from "@/components/common";
import { FilterSelect, ProjectSelect } from "@/components/project-select";
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
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useT } from "@/i18n/react";
import type { MessageKey } from "@/i18n/dictionaries";
import { useActiveProject } from "@/lib/use-active-project";
import { api } from "@/lib/api";
import { formatBytes, formatDateTime } from "@/lib/format";
import { assetQuery, qk, useDeleteAsset } from "@/lib/queries";
import { ASSET_KINDS, ASSET_STATUSES, type Asset, type Rendition } from "@/lib/types";
import { cn } from "@/lib/utils";

const route = getRouteApi("/app/assets");
const PAGE = 30;

type Tone = "success" | "danger" | "warning" | "neutral" | "info";
const STATUS_TONE: Record<string, Tone> = { ready: "success", failed: "danger", processing: "info" };

const ENUM_KEY: Record<string, MessageKey> = {
  ready: "assets.status.ready",
  failed: "assets.status.failed",
  processing: "assets.status.processing",
  image: "assets.kind.image",
  video: "assets.kind.video",
  file: "assets.kind.file",
  public: "assets.visibility.public",
  private: "assets.visibility.private",
};

/** Translated label for an API enum value; unknown values are shown as-is. */
function useEnumLabel() {
  const t = useT();
  return (v: string) => {
    const k = ENUM_KEY[v];
    return k ? t(k) : v;
  };
}

const NON_IMAGE = new Set(["mp4", "webm", "m3u8", "pdf", "docx", "xlsx", "pptx", "odt", "ods", "odp", "rtf", "txt", "csv"]);

/** Image-like rendition for previews; for documents this is the thumb (avif/webp), never the file itself. */
function previewRendition(a: Asset): Rendition | undefined {
  return a.renditions.find((r) => r.url && !NON_IMAGE.has(r.format));
}

/** The document itself: the "main" rendition of a file asset. */
function mainFile(a: Asset): Rendition | undefined {
  return a.kind === "file" ? (a.renditions.find((r) => r.name === "main") ?? a.renditions.find((r) => NON_IMAGE.has(r.format))) : undefined;
}

function Thumb({ asset, className }: { asset: Asset; className?: string }) {
  const src = (asset.poster?.url || previewRendition(asset)?.url) ?? undefined;
  const Icon = asset.kind === "video" ? Video : asset.kind === "file" ? FileText : ImageOff;
  return (
    <div
      className={cn("flex size-10 shrink-0 items-center justify-center overflow-hidden rounded-md bg-muted text-muted-foreground", className)}
      style={!src && asset.placeholder?.dominant_color ? { backgroundColor: asset.placeholder.dominant_color } : undefined}
    >
      {src ? <img src={src} alt="" loading="lazy" className="size-full object-cover" /> : <Icon className="size-4 opacity-60" />}
    </div>
  );
}

function Preview({ asset }: { asset: Asset }) {
  const t = useT();
  const video = asset.renditions.find((r) => r.url && (r.format === "mp4" || r.format === "webm"));
  const img = previewRendition(asset);
  if (asset.status === "failed") return null;
  if (asset.kind === "video" && video?.url) {
    return (
      <video
        src={video.url}
        poster={asset.poster?.url}
        controls
        preload="metadata"
        className="max-h-72 w-full rounded-lg border bg-black"
      />
    );
  }
  if (img?.url) {
    return (
      <div className="flex justify-center rounded-lg border bg-muted/40 p-2">
        <img src={img.url} alt={asset.original.filename ?? asset.id} className="max-h-72 rounded object-contain" />
      </div>
    );
  }
  if (asset.kind === "file" && asset.visibility === "public") {
    return (
      <div className="flex h-32 items-center justify-center rounded-lg border bg-muted/40 text-muted-foreground">
        <FileText className="size-10 opacity-50" aria-hidden />
      </div>
    );
  }
  if (asset.visibility !== "public") {
    return (
      <p className="rounded-lg border border-dashed p-4 text-center text-xs text-muted-foreground">
        {t("assets.privatePreview")}
      </p>
    );
  }
  return null;
}

function AssetDetail({ projectId, assetId, onClose }: { projectId: string; assetId: string | null; onClose: () => void }) {
  const asset = useQuery({ ...assetQuery(projectId, assetId ?? ""), enabled: !!assetId });
  const del = useDeleteAsset(projectId);
  const [confirm, setConfirm] = useState(false);
  const t = useT();
  const enumLabel = useEnumLabel();
  const a = asset.data;

  return (
    <Sheet open={!!assetId} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="sm:max-w-lg">
        <SheetHeader>
          <SheetTitle className="pr-6 truncate">{a?.original.filename ?? assetId}</SheetTitle>
          <SheetDescription className="font-mono text-xs break-all">{assetId}</SheetDescription>
        </SheetHeader>
        <div className="flex-1 space-y-5 overflow-y-auto px-4 pb-4">
          {asset.isPending ? (
            <>
              <Skeleton className="h-48 w-full" />
              <Skeleton className="h-24 w-full" />
            </>
          ) : asset.isError ? (
            <ErrorState error={asset.error} onRetry={() => void asset.refetch()} />
          ) : a ? (
            <>
              <Preview asset={a} />
              {a.error && (
                <div role="alert" className="rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-sm">
                  <div className="font-mono text-xs font-semibold text-destructive">{a.error.code}</div>
                  {a.error.detail && <p className="mt-1 text-muted-foreground">{a.error.detail}</p>}
                </div>
              )}
              <dl className="divide-y">
                <KeyValue label={t("assets.status")}>
                  <StatusBadge tone={STATUS_TONE[a.status] ?? "neutral"}>{enumLabel(a.status)}</StatusBadge>
                </KeyValue>
                <KeyValue label={t("assets.kind")}>{enumLabel(a.kind)}</KeyValue>
                <KeyValue label={t("assets.namespace")} mono>
                  {a.namespace}
                </KeyValue>
                <KeyValue label={t("assets.visibility")}>{enumLabel(a.visibility)}</KeyValue>
                <KeyValue label={t("assets.preset")}>
                  {a.preset ? `${a.preset} v${a.preset_version}` : "—"}
                </KeyValue>
                <KeyValue label={t("assets.original")}>
                  {formatBytes(a.original.bytes)}
                  {a.original.width && a.original.height ? ` · ${a.original.width}×${a.original.height}` : ""}
                </KeyValue>
                <KeyValue label={t("assets.savings")}>
                  {a.savings_ratio === null ? "—" : `${(a.savings_ratio * 100).toFixed(1)}%`}
                </KeyValue>
                {a.duration_sec !== null && <KeyValue label={t("assets.duration")}>{t("assets.seconds", { s: a.duration_sec.toFixed(1) })}</KeyValue>}
                <KeyValue label={t("assets.created")}>{formatDateTime(a.created_at)}</KeyValue>
                <KeyValue label={t("assets.ready")}>{formatDateTime(a.ready_at)}</KeyValue>
              </dl>

              {a.kind === "file" && mainFile(a) && (
                <section className="rounded-lg border p-3">
                  <h4 className="mb-2 text-sm font-semibold">{t("assets.document")}</h4>
                  <div className="flex items-center gap-3 text-sm">
                    <FileText className="size-5 shrink-0 text-muted-foreground" aria-hidden />
                    <div className="min-w-0 flex-1">
                      <div className="font-medium">
                        <span className="font-mono text-xs uppercase">{mainFile(a)!.format}</span> · {formatBytes(mainFile(a)!.bytes)}
                      </div>
                      {a.savings_ratio !== null && a.savings_ratio > 0 && (
                        <div className="text-xs text-success">
                          {t("assets.smaller", { pct: (a.savings_ratio * 100).toFixed(0) })} ({formatBytes(a.original.bytes)} → {formatBytes(mainFile(a)!.bytes)})
                        </div>
                      )}
                    </div>
                    {mainFile(a)!.url && (
                      <Button size="sm" variant="outline" asChild>
                        <a href={mainFile(a)!.url} target="_blank" rel="noreferrer" download>
                          <Download /> {t("assets.download")}
                        </a>
                      </Button>
                    )}
                  </div>
                </section>
              )}

              <section>
                <h4 className="mb-2 text-sm font-semibold">{t("assets.renditions")}</h4>
                {a.renditions.length === 0 ? (
                  <p className="text-sm text-muted-foreground">{t("assets.noRenditions")}</p>
                ) : (
                  <ul className="divide-y rounded-lg border">
                    {a.renditions.map((r) => (
                      <li key={r.name + r.format} className="flex items-center gap-3 px-3 py-2 text-sm">
                        <div className="min-w-0 flex-1">
                          <div className="font-medium">
                            {r.name} <span className="font-mono text-xs text-muted-foreground uppercase">{r.format}</span>
                          </div>
                          <div className="truncate font-mono text-xs text-muted-foreground" title={r.url ?? r.key}>
                            {r.width && r.height ? `${r.width}×${r.height} · ` : ""}
                            {r.url ?? r.key}
                          </div>
                        </div>
                        <div className="text-right text-xs tabular-nums">
                          <div className="font-medium">{formatBytes(r.bytes)}</div>
                          {a.original.bytes > 0 && (
                            <div className="text-muted-foreground">
                              {t("assets.smaller", { pct: (((a.original.bytes - r.bytes) / a.original.bytes) * 100).toFixed(0) })}
                            </div>
                          )}
                        </div>
                        {(r.url ?? r.key) && <CopyButton value={(r.url ?? r.key)!} label={t("assets.copyLocation", { name: r.name })} size="icon-xs" />}
                      </li>
                    ))}
                  </ul>
                )}
              </section>

              <section>
                <h4 className="mb-2 text-sm font-semibold">{t("assets.metadata")}</h4>
                <pre className="max-h-48 overflow-auto rounded-lg bg-muted/60 p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap">
                  {JSON.stringify(a.metadata ?? {}, null, 2)}
                </pre>
              </section>

              <Button variant="outline" className="text-destructive hover:text-destructive" onClick={() => setConfirm(true)}>
                <Trash2 /> {t("assets.delete")}
              </Button>
            </>
          ) : null}
        </div>

        <AlertDialog open={confirm} onOpenChange={setConfirm}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{t("assets.delete.title")}</AlertDialogTitle>
              <AlertDialogDescription>
                {t("assets.delete.desc")}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>{t("assets.cancel")}</AlertDialogCancel>
              <AlertDialogAction
                className="bg-destructive text-white hover:bg-destructive/90"
                onClick={() => {
                  if (assetId) del.mutate(assetId, { onSuccess: onClose });
                  setConfirm(false);
                }}
              >
                {t("assets.delete")}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </SheetContent>
    </Sheet>
  );
}

export default function AssetsPage() {
  const t = useT();
  const enumLabel = useEnumLabel();
  const { project } = route.useSearch();
  const navigate = route.useNavigate();
  const setProject = useCallback((id: string) => void navigate({ search: { project: id }, replace: true }), [navigate]);
  const { projects, list, active } = useActiveProject(project, setProject);

  const [namespace, setNamespace] = useState("");
  const [kind, setKind] = useState<string | undefined>();
  const [status, setStatus] = useState<string | undefined>();
  const [selected, setSelected] = useState<string | null>(null);
  const ns = namespace.trim() || undefined;

  const pid = active?.id ?? "";
  const assets = useInfiniteQuery({
    queryKey: [...qk.assets(pid), { ns, kind, status }],
    queryFn: ({ pageParam }) => api.assets.list(pid, { namespace: ns, kind, status, limit: PAGE, cursor: pageParam }),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.next_cursor || null,
    enabled: !!pid,
  });
  const items = assets.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <>
      <PageHeader
        title={t("assets.title")}
        description={t("assets.description")}
        actions={<ProjectSelect projects={list} value={active?.id} onChange={setProject} loading={projects.isPending} />}
      />

      {projects.isError ? (
        <ErrorState error={projects.error} onRetry={() => void projects.refetch()} />
      ) : projects.isSuccess && list.length === 0 ? (
        <EmptyState icon={<FolderKanban />} title={t("assets.noProjects.title")} description={t("assets.noProjects.desc")} />
      ) : (
        <>
          <div className="mb-4 flex flex-wrap items-center gap-2">
            <Input
              value={namespace}
              onChange={(e) => setNamespace(e.target.value)}
              placeholder={t("assets.namespace")}
              aria-label={t("assets.namespace")}
              className="h-8 w-44"
            />
            <FilterSelect value={kind} onChange={setKind} options={ASSET_KINDS} allLabel={t("assets.allKinds")} label={t("assets.kind")} optionLabel={enumLabel} />
            <FilterSelect value={status} onChange={setStatus} options={ASSET_STATUSES} allLabel={t("assets.allStatuses")} label={t("assets.status")} optionLabel={enumLabel} />
          </div>

          {assets.isPending ? (
            <div className="space-y-2 rounded-xl border bg-card p-4">
              {Array.from({ length: 6 }, (_, i) => (
                <Skeleton key={i} className="h-12 w-full" />
              ))}
            </div>
          ) : assets.isError ? (
            <ErrorState error={assets.error} onRetry={() => void assets.refetch()} />
          ) : items.length === 0 ? (
            <EmptyState
              icon={<Images />}
              title={t("assets.empty.title")}
              description={ns || kind || status ? t("assets.empty.filtered") : t("assets.empty.desc")}
            />
          ) : (
            <>
              <div className="overflow-hidden rounded-xl border bg-card">
                <Table>
                  <TableHeader>
                    <TableRow className="hover:bg-transparent">
                      <TableHead className="pl-4">{t("assets.col.asset")}</TableHead>
                      <TableHead>{t("assets.status")}</TableHead>
                      <TableHead className="hidden md:table-cell">{t("assets.namespace")}</TableHead>
                      <TableHead className="hidden sm:table-cell">{t("assets.col.size")}</TableHead>
                      <TableHead className="hidden lg:table-cell">{t("assets.preset")}</TableHead>
                      <TableHead className="hidden lg:table-cell pr-4">{t("assets.created")}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {items.map((a) => (
                      <TableRow
                        key={a.id}
                        tabIndex={0}
                        className="cursor-pointer"
                        onClick={() => setSelected(a.id)}
                        onKeyDown={(e) => e.key === "Enter" && setSelected(a.id)}
                      >
                        <TableCell className="pl-4">
                          <div className="flex items-center gap-3">
                            <Thumb asset={a} />
                            <div className="min-w-0">
                              <div className="max-w-[16rem] truncate text-sm font-medium">{a.original.filename ?? a.id}</div>
                              <div className="font-mono text-xs text-muted-foreground">{enumLabel(a.kind)}</div>
                            </div>
                          </div>
                        </TableCell>
                        <TableCell>
                          <StatusBadge tone={STATUS_TONE[a.status] ?? "neutral"}>{enumLabel(a.status)}</StatusBadge>
                        </TableCell>
                        <TableCell className="hidden font-mono text-xs md:table-cell">{a.namespace}</TableCell>
                        <TableCell className="hidden text-sm tabular-nums sm:table-cell">
                          {formatBytes(a.original.bytes)}
                          {a.savings_ratio !== null && a.savings_ratio > 0 && (
                            <span className="ml-1.5 text-xs text-success">−{(a.savings_ratio * 100).toFixed(0)}%</span>
                          )}
                        </TableCell>
                        <TableCell className="hidden font-mono text-xs lg:table-cell">
                          {a.preset ? `${a.preset} v${a.preset_version}` : "—"}
                        </TableCell>
                        <TableCell className="hidden pr-4 text-sm text-muted-foreground lg:table-cell">
                          <RelativeTime value={a.created_at} />
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
              <div className="mt-4 flex items-center justify-between text-sm text-muted-foreground">
                <span>
                  {t("assets.count", { count: items.length })}
                </span>
                {assets.hasNextPage ? (
                  <Button variant="outline" size="sm" onClick={() => void assets.fetchNextPage()} disabled={assets.isFetchingNextPage}>
                    {assets.isFetchingNextPage && <Loader2 className="animate-spin" />}
                    {t("assets.loadMore")}
                  </Button>
                ) : (
                  <span>{t("assets.endOfList")}</span>
                )}
              </div>
            </>
          )}
        </>
      )}

      {pid && <AssetDetail projectId={pid} assetId={selected} onClose={() => setSelected(null)} />}
    </>
  );
}
