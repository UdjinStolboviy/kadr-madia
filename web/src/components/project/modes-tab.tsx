import { useQuery } from "@tanstack/react-query";
import { FileText, Film, ImageIcon, Loader2, Pencil, RotateCcw } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import { StatusBadge } from "@/components/common";
import { PresetEditor } from "@/components/project/preset-editor";
import { ErrorState } from "@/components/states";
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
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { useT } from "@/i18n/react";
import type { MessageKey } from "@/i18n/dictionaries";
import { errorMessage } from "@/lib/api";
import { summarizePreset } from "@/lib/presets";
import { modesQuery, useResetPreset, useUpdateProject } from "@/lib/queries";
import { COMPRESSION_MODES, type CompressionMode, type ModeInfo, type Preset, type Project } from "@/lib/types";
import { cn } from "@/lib/utils";

const TITLES: Record<CompressionMode, { title: MessageKey; blurb: MessageKey }> = {
  compact: { title: "modes.compact.title", blurb: "modes.compact.blurb" },
  balanced: { title: "modes.balanced.title", blurb: "modes.balanced.blurb" },
  quality: { title: "modes.quality.title", blurb: "modes.quality.blurb" },
};

const FACT_LABELS: Record<"image" | "video" | "file", MessageKey[]> = {
  image: ["common.preset.resize", "common.preset.quality", "common.preset.formats"],
  video: ["common.preset.video", "common.preset.frame", "common.preset.audio"],
  file: ["common.preset.fileOptimize", "common.preset.fileThumb", "common.preset.input"],
};

function PresetColumn({
  kind,
  preset,
  customized,
  onEdit,
  onReset,
  resetting,
}: {
  kind: "image" | "video" | "file";
  preset: Preset | undefined;
  customized: boolean;
  onEdit: () => void;
  onReset: () => void;
  resetting: boolean;
}) {
  const t = useT();
  const Icon = kind === "video" ? Film : kind === "file" ? FileText : ImageIcon;
  const kindLabel = kind === "video" ? t("modes.kindVideo") : kind === "file" ? t("modes.kindFile") : t("modes.kindImage");
  if (!preset) {
    return (
      <div className="rounded-lg border border-dashed p-3 text-sm text-muted-foreground">
        <span className="font-medium">{kindLabel}</span>: {t("modes.notCreated")}
      </div>
    );
  }
  const all = summarizePreset(preset.kind, preset.params).facts;
  const facts = FACT_LABELS[kind].flatMap((l) => all.filter((f) => f.label === t(l)));
  return (
    <div className="flex min-w-0 flex-col gap-3 rounded-lg border bg-muted/20 p-3">
      <div className="flex flex-wrap items-center gap-2">
        <Icon className={cn("size-4", kind === "video" ? "text-warning" : kind === "file" ? "text-success" : "text-brand")} />
        <span className="text-sm font-semibold">{kindLabel}</span>
        <StatusBadge tone="neutral" dot={false}>
          v{preset.current_version}
        </StatusBadge>
        {customized && (
          <StatusBadge tone="warning" dot={false}>
            {t("modes.customized")}
          </StatusBadge>
        )}
      </div>
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
        {facts.map((f) => (
          <div key={f.label} className="contents">
            <dt className="text-muted-foreground">{f.label}</dt>
            <dd className="min-w-0 break-words">{f.value}</dd>
          </div>
        ))}
      </dl>
      <div className="mt-auto flex flex-wrap gap-2 pt-1">
        <Button size="sm" variant="outline" onClick={onEdit} aria-label={t("modes.editAria", { slug: preset.slug })}>
          <Pencil /> {t("modes.edit")}
        </Button>
        <Button size="sm" variant="ghost" onClick={onReset} disabled={!customized || resetting} aria-label={t("modes.resetAria", { slug: preset.slug })}>
          {resetting ? <Loader2 className="animate-spin" /> : <RotateCcw />} {t("modes.resetDefaults")}
        </Button>
      </div>
    </div>
  );
}

function ModeCard({ info, isDefault, onEdit, onReset, resettingSlug }: { info: ModeInfo; isDefault: boolean; onEdit: (p: Preset) => void; onReset: (p: Preset) => void; resettingSlug: string | null }) {
  const t = useT();
  const names = TITLES[info.mode];
  return (
    <Card className="gap-4 py-5">
      <CardHeader className="px-5">
        <CardTitle className="flex items-center gap-2 text-base">
          {t(names.title)}
          {isDefault && <StatusBadge tone="success" dot={false}>{t("modes.default")}</StatusBadge>}
        </CardTitle>
        <CardDescription>{t(names.blurb)}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3 px-5 md:grid-cols-2 xl:grid-cols-3">
        <PresetColumn kind="image" preset={info.image} customized={info.customized.image} onEdit={() => onEdit(info.image)} onReset={() => onReset(info.image)} resetting={resettingSlug === info.image?.slug} />
        <PresetColumn kind="video" preset={info.video} customized={info.customized.video} onEdit={() => onEdit(info.video)} onReset={() => onReset(info.video)} resetting={resettingSlug === info.video?.slug} />
        <PresetColumn kind="file" preset={info.file} customized={info.customized.file === true} onEdit={() => info.file && onEdit(info.file)} onReset={() => info.file && onReset(info.file)} resetting={resettingSlug === info.file?.slug} />
      </CardContent>
    </Card>
  );
}

export function ModesTab({ project }: { project: Project }) {
  const t = useT();
  const modes = useQuery(modesQuery(project.id));
  const update = useUpdateProject(project.id);
  const reset = useResetPreset(project.id);
  const [editing, setEditing] = useState<Preset | null>(null);
  const [toReset, setToReset] = useState<Preset | null>(null);

  if (modes.isPending) {
    return (
      <div className="grid gap-4">
        <Skeleton className="h-16 rounded-xl" />
        {[0, 1, 2].map((i) => (
          <Skeleton key={i} className="h-60 rounded-xl" />
        ))}
      </div>
    );
  }
  if (modes.isError) return <ErrorState error={modes.error} onRetry={() => void modes.refetch()} />;

  const current = project.default_mode ?? modes.data.default_mode;
  const byMode = new Map(modes.data.modes.map((m) => [m.mode, m]));

  return (
    <div className="grid gap-4">
      <div className="flex flex-col gap-3 rounded-xl border bg-card p-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold">{t("modes.defaultMode")}</h2>
          <p className="text-sm text-muted-foreground">
            {t("modes.defaultDescPre")} <code className="font-mono text-xs">mode</code>.
          </p>
        </div>
        <Select
          value={current}
          disabled={update.isPending || project.disabled}
          onValueChange={(v) =>
            update.mutate(
              { default_mode: v as CompressionMode },
              {
                onSuccess: () => toast.success(t("modes.defaultSet", { mode: t(TITLES[v as CompressionMode].title) })),
                onError: (e) => toast.error(t("modes.defaultFailed"), { description: errorMessage(e) }),
              },
            )
          }
        >
          <SelectTrigger className="w-full sm:w-48" aria-label={t("modes.defaultMode")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {COMPRESSION_MODES.map((m) => (
              <SelectItem key={m} value={m}>
                {t(TITLES[m].title)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {COMPRESSION_MODES.map((m) => {
        const info = byMode.get(m);
        return info ? (
          <ModeCard key={m} info={info} isDefault={m === current} onEdit={setEditing} onReset={setToReset} resettingSlug={reset.isPending ? (reset.variables ?? null) : null} />
        ) : null;
      })}

      <PresetEditor projectId={project.id} preset={editing} open={!!editing} onOpenChange={(o) => !o && setEditing(null)} />

      <AlertDialog open={!!toReset} onOpenChange={(o) => !o && setToReset(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("modes.resetTitle", { slug: toReset?.slug ?? "" })}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("modes.resetDesc")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("modes.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (toReset) reset.mutate(toReset.slug);
                setToReset(null);
              }}
            >
              {t("modes.reset")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
