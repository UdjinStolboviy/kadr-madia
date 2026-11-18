import { useQuery } from "@tanstack/react-query";
import { ChevronDown, Loader2, Sparkles } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import { Field, StatusBadge } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import type { MessageKey } from "@/i18n/dictionaries";
import { useT } from "@/i18n/react";
import { errorMessage, isApiError } from "@/lib/api";
import { formatDate } from "@/lib/format";
import { moderationSettingsQuery, useModerationBacklog, useSaveModerationSettings } from "@/lib/queries";
import { AI_MODERATION_MODES, ASSET_KINDS, type AiModerationMode, type ModerationSettingsResponse } from "@/lib/types";
import { useRole } from "@/lib/use-role";
import { cn } from "@/lib/utils";

/** Per-project automatic AI moderation settings (collapsible card). */
export function AiModerationCard({ projectId }: { projectId: string }) {
  const t = useT();
  const settings = useQuery(moderationSettingsQuery(projectId));
  const [open, setOpen] = useState(false);
  const enabled = settings.data?.settings.enabled ?? false;

  return (
    <Collapsible open={open} onOpenChange={setOpen} className="mb-4 rounded-xl border bg-card">
      <CollapsibleTrigger asChild>
        <button
          type="button"
          className="flex w-full items-center justify-between gap-3 rounded-xl px-4 py-3 text-left outline-none hover:bg-accent/40 focus-visible:ring-2 focus-visible:ring-ring/50"
        >
          <span className="flex min-w-0 items-center gap-2">
            <Sparkles className="size-4 shrink-0 text-brand" />
            <span className="text-sm font-semibold">{t("moderation.ai.title")}</span>
            {settings.data && (
              <StatusBadge tone={enabled ? "success" : "neutral"}>{enabled ? t("moderation.ai.on") : t("moderation.ai.off")}</StatusBadge>
            )}
          </span>
          <ChevronDown className={cn("size-4 shrink-0 text-muted-foreground transition-transform", open && "rotate-180")} />
        </button>
      </CollapsibleTrigger>
      <CollapsibleContent className="border-t px-4 py-4">
        {settings.isPending ? (
          <Skeleton className="h-40 w-full" />
        ) : settings.isError ? (
          <p className="text-sm text-destructive">{errorMessage(settings.error)}</p>
        ) : (
          <SettingsForm key={projectId + JSON.stringify(settings.data.settings)} projectId={projectId} data={settings.data} />
        )}
      </CollapsibleContent>
    </Collapsible>
  );
}

function SettingsForm({ projectId, data }: { projectId: string; data: ModerationSettingsResponse }) {
  const t = useT();
  const { canManage } = useRole();
  const save = useSaveModerationSettings(projectId);
  const backlog = useModerationBacklog(projectId);
  const [enabled, setEnabled] = useState(data.settings.enabled);
  const [policy, setPolicy] = useState(data.settings.policy);
  const [mode, setMode] = useState<AiModerationMode>(data.settings.mode);
  const [kinds, setKinds] = useState<string[]>(data.settings.kinds);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const ai = data.ai;
  const editable = canManage && (ai.available || !enabled);

  async function submit(ev: React.FormEvent) {
    ev.preventDefault();
    setErrors({});
    try {
      await save.mutateAsync({ enabled, policy, mode, kinds });
    } catch (err) {
      if (isApiError(err, "validation_failed") && Object.keys(err.fieldErrors).length) setErrors(err.fieldErrors);
      else toast.error(t("moderation.ai.saveFailed"), { description: errorMessage(err) });
    }
  }

  return (
    <form onSubmit={submit} noValidate className="grid gap-4">
      <p className="text-sm text-muted-foreground">{t("moderation.ai.desc")}</p>
      {!ai.available ? (
        <p className="rounded-lg border border-warning/40 bg-warning/5 px-3 py-2 text-sm text-warning">{t("moderation.ai.notConfigured")}</p>
      ) : (
        ai.classifier && <p className="text-xs text-muted-foreground">{t("moderation.ai.classifier", { name: ai.classifier })}</p>
      )}

      <label className="flex items-center justify-between gap-3 text-sm font-medium">
        <span>{t("moderation.ai.enabled")}</span>
        <Switch checked={enabled} disabled={!canManage || (!ai.available && !enabled)} onCheckedChange={setEnabled} />
      </label>
      {errors.enabled && (
        <p className="text-xs text-destructive" role="alert">
          {errors.enabled}
        </p>
      )}
      {data.settings.enabled && data.settings.since && (
        <p className="text-xs text-muted-foreground">{t("moderation.ai.since", { when: formatDate(data.settings.since) })}</p>
      )}

      <Field label={t("moderation.ai.policy")} htmlFor="ai-policy" hint={t("moderation.ai.policyHint")} error={errors.policy}>
        <Textarea id="ai-policy" rows={4} value={policy} disabled={!editable} onChange={(e) => setPolicy(e.target.value)} />
      </Field>

      <fieldset className="grid gap-2">
        <legend className="mb-1.5 text-sm font-medium">{t("moderation.ai.mode")}</legend>
        <div className="grid gap-1 rounded-lg border p-1" role="radiogroup">
          {AI_MODERATION_MODES.map((m) => (
            <label key={m} className={cn("flex cursor-pointer items-start gap-3 rounded-md px-2.5 py-2 hover:bg-accent/60", mode === m && "bg-accent/50")}>
              <input
                type="radio"
                name="ai-mode"
                value={m}
                checked={mode === m}
                disabled={!editable}
                onChange={() => setMode(m)}
                className="mt-1 accent-[var(--color-brand)]"
              />
              <span className="grid gap-0.5">
                <span className="text-sm font-medium">{t(`moderation.ai.mode.${m}` as MessageKey)}</span>
                <span className="text-xs text-muted-foreground">{t(`moderation.ai.mode.${m}Desc` as MessageKey)}</span>
              </span>
            </label>
          ))}
        </div>
      </fieldset>

      <fieldset className="grid gap-2">
        <legend className="mb-1 text-sm font-medium">{t("moderation.ai.kinds")}</legend>
        <div className="flex flex-wrap gap-4">
          {ASSET_KINDS.map((k) => (
            <label key={k} className="flex items-center gap-2 text-sm">
              <Checkbox
                checked={kinds.includes(k)}
                disabled={!editable}
                onCheckedChange={(c) => setKinds(c ? [...kinds, k] : kinds.filter((x) => x !== k))}
              />
              {k}
            </label>
          ))}
        </div>
        {errors.kinds && <p className="text-xs text-destructive">{errors.kinds}</p>}
      </fieldset>

      {canManage && (
        <div className="flex flex-wrap justify-end gap-2 border-t pt-4">
          <Button type="button" variant="outline" disabled={!ai.available || backlog.isPending} onClick={() => backlog.mutate()}>
            {backlog.isPending && <Loader2 className="animate-spin" />}
            {t("moderation.ai.backlog")}
          </Button>
          <Button type="submit" disabled={save.isPending}>
            {save.isPending && <Loader2 className="animate-spin" />}
            {t("moderation.ai.save")}
          </Button>
        </div>
      )}
    </form>
  );
}
