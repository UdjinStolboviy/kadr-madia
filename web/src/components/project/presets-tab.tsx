import { useQuery } from "@tanstack/react-query";
import { ChevronDown, Film, ImageIcon, Layers } from "lucide-react";
import { useState } from "react";

import { CopyButton, StatusBadge } from "@/components/common";
import { EmptyState, ErrorState } from "@/components/states";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Skeleton } from "@/components/ui/skeleton";
import { useT } from "@/i18n/react";
import { presetsQuery } from "@/lib/queries";
import { isModeSlug, summarizePreset } from "@/lib/presets";
import type { Preset } from "@/lib/types";
import { cn } from "@/lib/utils";

function PresetCard({ preset }: { preset: Preset }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const summary = summarizePreset(preset.kind, preset.params);
  const json = JSON.stringify(preset.params, null, 2);
  const Icon = preset.kind === "video" ? Film : ImageIcon;

  return (
    <Card className="gap-4 py-5">
      <CardHeader className="px-5">
        <div className="flex items-start gap-3">
          <div
            className={cn(
              "flex size-9 shrink-0 items-center justify-center rounded-lg",
              preset.kind === "video" ? "bg-warning/10 text-warning" : "bg-brand/10 text-brand",
            )}
          >
            <Icon className="size-4" />
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="font-mono text-sm font-semibold">{preset.slug}</h3>
              <StatusBadge tone="neutral" dot={false}>
                v{preset.current_version}
              </StatusBadge>
              {isModeSlug(preset.slug) && (
                <StatusBadge tone="info" dot={false}>
                  {t("presets.modePreset")}
                </StatusBadge>
              )}
            </div>
            {preset.description && <p className="mt-0.5 text-sm text-muted-foreground">{preset.description}</p>}
          </div>
        </div>
      </CardHeader>
      <CardContent className="grid gap-4 px-5">
        <p className="rounded-md bg-muted/60 px-3 py-2 text-sm font-medium">{summary.headline}</p>
        {summary.facts.length > 0 && (
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
            {summary.facts.map((f) => (
              <div key={f.label} className="contents">
                <dt className="text-muted-foreground">{f.label}</dt>
                <dd className="min-w-0 break-words">{f.value}</dd>
              </div>
            ))}
          </dl>
        )}
        <Collapsible open={open} onOpenChange={setOpen}>
          <div className="flex items-center justify-between">
            <CollapsibleTrigger className="inline-flex items-center gap-1 rounded-sm text-xs font-medium text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50">
              <ChevronDown className={cn("size-3.5 transition-transform", open && "rotate-180")} />
              {open ? t("presets.hideJson") : t("presets.showJson")}
            </CollapsibleTrigger>
            {open && <CopyButton value={json} label={t("presets.copyJson")} size="icon-xs" />}
          </div>
          <CollapsibleContent>
            <pre className="mt-2 max-h-80 overflow-auto rounded-md border bg-muted/40 p-3 font-mono text-xs leading-relaxed">
              {json}
            </pre>
          </CollapsibleContent>
        </Collapsible>
      </CardContent>
    </Card>
  );
}

export function PresetsTab({ projectId }: { projectId: string }) {
  const t = useT();
  const presets = useQuery(presetsQuery(projectId));

  if (presets.isPending) {
    return (
      <div className="grid gap-4 md:grid-cols-2">
        {Array.from({ length: 4 }, (_, i) => (
          <Skeleton key={i} className="h-56 rounded-xl" />
        ))}
      </div>
    );
  }
  if (presets.isError) return <ErrorState error={presets.error} onRetry={() => void presets.refetch()} />;
  if (presets.data.length === 0) {
    return <EmptyState icon={<Layers />} title={t("presets.emptyTitle")} description={t("presets.emptyDesc")} />;
  }

  const images = presets.data.filter((p) => p.kind === "image");
  const videos = presets.data.filter((p) => p.kind === "video");

  return (
    <div className="grid gap-8">
      <p className="text-sm text-muted-foreground">
        {t("presets.intro")}
      </p>
      {[
        { id: "image", title: t("presets.imageGroup"), items: images },
        { id: "video", title: t("presets.videoGroup"), items: videos },
      ]
        .filter((g) => g.items.length)
        .map((g) => (
          <section key={g.id}>
            <h2 className="mb-3 text-sm font-semibold">{g.title}</h2>
            <div className="grid gap-4 md:grid-cols-2">
              {g.items.map((p) => (
                <PresetCard key={p.id} preset={p} />
              ))}
            </div>
          </section>
        ))}
    </div>
  );
}
