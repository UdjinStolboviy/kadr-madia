import { useQuery } from "@tanstack/react-query";
import { Film, ImageIcon, Loader2, Pencil, RotateCcw } from "lucide-react";
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
import { errorMessage } from "@/lib/api";
import { summarizePreset } from "@/lib/presets";
import { modesQuery, useResetPreset, useUpdateProject } from "@/lib/queries";
import { COMPRESSION_MODES, type CompressionMode, type ModeInfo, type Preset, type Project } from "@/lib/types";
import { cn } from "@/lib/utils";

const TITLES: Record<CompressionMode, { title: string; blurb: string }> = {
  compact: { title: "Compact", blurb: "Good quality, very small" },
  balanced: { title: "Balanced", blurb: "The golden middle" },
  quality: { title: "Quality", blurb: "Ideal quality, smallest size that achieves it" },
};

const FACT_LABELS: Record<"image" | "video", string[]> = {
  image: ["Resize", "Quality", "Formats"],
  video: ["Video", "Frame", "Audio"],
};

function PresetColumn({
  kind,
  preset,
  customized,
  onEdit,
  onReset,
  resetting,
}: {
  kind: "image" | "video";
  preset: Preset | undefined;
  customized: boolean;
  onEdit: () => void;
  onReset: () => void;
  resetting: boolean;
}) {
  const Icon = kind === "video" ? Film : ImageIcon;
  if (!preset) {
    return (
      <div className="rounded-lg border border-dashed p-3 text-sm text-muted-foreground">
        <span className="font-medium capitalize">{kind}</span> preset not created yet. It appears after the next server start.
      </div>
    );
  }
  const all = summarizePreset(preset.kind, preset.params).facts;
  const facts = FACT_LABELS[kind].flatMap((l) => all.filter((f) => f.label === l));
  return (
    <div className="flex min-w-0 flex-col gap-3 rounded-lg border bg-muted/20 p-3">
      <div className="flex flex-wrap items-center gap-2">
        <Icon className={cn("size-4", kind === "video" ? "text-warning" : "text-brand")} />
        <span className="text-sm font-semibold capitalize">{kind}</span>
        <StatusBadge tone="neutral" dot={false}>
          v{preset.current_version}
        </StatusBadge>
        {customized && (
          <StatusBadge tone="warning" dot={false}>
            customized
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
        <Button size="sm" variant="outline" onClick={onEdit} aria-label={`Edit ${preset.slug}`}>
          <Pencil /> Edit
        </Button>
        <Button size="sm" variant="ghost" onClick={onReset} disabled={!customized || resetting} aria-label={`Reset ${preset.slug} to defaults`}>
          {resetting ? <Loader2 className="animate-spin" /> : <RotateCcw />} Reset to defaults
        </Button>
      </div>
    </div>
  );
}

function ModeCard({ info, isDefault, onEdit, onReset, resettingSlug }: { info: ModeInfo; isDefault: boolean; onEdit: (p: Preset) => void; onReset: (p: Preset) => void; resettingSlug: string | null }) {
  const t = TITLES[info.mode];
  return (
    <Card className="gap-4 py-5">
      <CardHeader className="px-5">
        <CardTitle className="flex items-center gap-2 text-base">
          {t.title}
          {isDefault && <StatusBadge tone="success" dot={false}>default</StatusBadge>}
        </CardTitle>
        <CardDescription>{t.blurb}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3 px-5 md:grid-cols-2">
        <PresetColumn kind="image" preset={info.image} customized={info.customized.image} onEdit={() => onEdit(info.image)} onReset={() => onReset(info.image)} resetting={resettingSlug === info.image?.slug} />
        <PresetColumn kind="video" preset={info.video} customized={info.customized.video} onEdit={() => onEdit(info.video)} onReset={() => onReset(info.video)} resetting={resettingSlug === info.video?.slug} />
      </CardContent>
    </Card>
  );
}

export function ModesTab({ project }: { project: Project }) {
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
          <h2 className="text-sm font-semibold">Default mode</h2>
          <p className="text-sm text-muted-foreground">
            Used when an upload names neither a preset nor a mode. Request a mode per upload with <code className="font-mono text-xs">mode</code>.
          </p>
        </div>
        <Select
          value={current}
          disabled={update.isPending || project.disabled}
          onValueChange={(v) =>
            update.mutate(
              { default_mode: v as CompressionMode },
              {
                onSuccess: () => toast.success(`Default mode: ${TITLES[v as CompressionMode].title}`),
                onError: (e) => toast.error("Couldn't change default mode", { description: errorMessage(e) }),
              },
            )
          }
        >
          <SelectTrigger className="w-full sm:w-48" aria-label="Default mode">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {COMPRESSION_MODES.map((m) => (
              <SelectItem key={m} value={m}>
                {TITLES[m].title}
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
            <AlertDialogTitle>Reset “{toReset?.slug}” to defaults?</AlertDialogTitle>
            <AlertDialogDescription>
              Your customizations are replaced by the built-in parameters as a new version. Assets already processed are not changed.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (toReset) reset.mutate(toReset.slug);
                setToReset(null);
              }}
            >
              Reset
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
