import { zodResolver } from "@hookform/resolvers/zod";
import { useQuery } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { useEffect, useMemo } from "react";
import { Controller, useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

import { Field, KeyValue, RelativeTime } from "@/components/common";
import { StorageSelect, UsageBar } from "@/components/project-form";
import { NO_STORAGE, quotaSchema, validateOrigin } from "@/lib/forms";
import { TagInput } from "@/components/tag-input";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { useT } from "@/i18n/react";
import type { MessageKey } from "@/i18n/dictionaries";
import { errorMessage, isApiError } from "@/lib/api";
import { bytesToGb, formatDateTime, gbToBytes } from "@/lib/format";
import { storageQuery, useUpdateProject } from "@/lib/queries";
import type { Project, UpdateProjectRequest } from "@/lib/types";

const makeSchema = (t: (k: MessageKey) => string) => z.object({
  name: z.string().trim().min(1, t("settings.errName")).max(100),
  storage_profile_id: z.string(),
  quota_gb: quotaSchema,
  allowed_origins: z.array(z.string()),
});
type Values = z.infer<ReturnType<typeof makeSchema>>;

function toValues(p: Project): Values {
  const gb = bytesToGb(p.quota_bytes);
  return {
    name: p.name,
    storage_profile_id: p.storage_profile_id ?? NO_STORAGE,
    quota_gb: gb === null ? "" : String(gb),
    allowed_origins: p.allowed_origins ?? [],
  };
}

export function SettingsTab({ project }: { project: Project }) {
  const t = useT();
  const schema = useMemo(() => makeSchema(t), [t]);
  const storage = useQuery(storageQuery);
  const update = useUpdateProject(project.id);
  const toggle = useUpdateProject(project.id);
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: toValues(project) });
  const { errors, isDirty } = form.formState;

  // Keep the form in sync when the project is refetched, unless the user is editing.
  useEffect(() => {
    if (!form.formState.isDirty) form.reset(toValues(project));
  }, [project, form]);

  async function onSubmit(v: Values) {
    const body: UpdateProjectRequest = {};
    const dirty = form.formState.dirtyFields;
    if (dirty.name) body.name = v.name;
    if (dirty.storage_profile_id && v.storage_profile_id !== NO_STORAGE) body.storage_profile_id = v.storage_profile_id;
    if (dirty.quota_gb) body.quota_bytes = v.quota_gb ? gbToBytes(Number(v.quota_gb)) : null;
    if (dirty.allowed_origins) body.allowed_origins = v.allowed_origins;
    try {
      const p = await update.mutateAsync(body);
      form.reset(toValues(p));
      toast.success(t("settings.saved"));
    } catch (e) {
      if (isApiError(e, "validation_failed") && Object.keys(e.fieldErrors).length) {
        for (const [k, msg] of Object.entries(e.fieldErrors)) {
          form.setError((k === "quota_bytes" ? "quota_gb" : k) as keyof Values, { message: msg });
        }
      } else {
        toast.error(t("settings.saveFailed"), { description: errorMessage(e) });
      }
    }
  }

  return (
    <div className="grid gap-6 lg:grid-cols-[1fr_18rem]">
      <div className="grid gap-6">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("settings.general")}</CardTitle>
            <CardDescription>{t("settings.generalDesc")}</CardDescription>
          </CardHeader>
          <CardContent>
            <form id="project-settings" onSubmit={form.handleSubmit(onSubmit)} noValidate className="grid gap-4">
              <Field label={t("settings.name")} htmlFor="ps-name" error={errors.name?.message}>
                <Input id="ps-name" aria-invalid={!!errors.name} {...form.register("name")} />
              </Field>
              <Field label={t("settings.slug")} htmlFor="ps-slug" hint={t("settings.slugHint")}>
                <Input id="ps-slug" value={project.slug} readOnly disabled className="font-mono" />
              </Field>
              <div className="grid gap-4 sm:grid-cols-2">
                <Field
                  label={t("settings.storageProfile")}
                  htmlFor="ps-storage"
                  hint={project.storage_profile_id ? t("settings.storageHint") : undefined}
                >
                  <Controller
                    control={form.control}
                    name="storage_profile_id"
                    render={({ field }) => (
                      <StorageSelect
                        id="ps-storage"
                        value={field.value}
                        onChange={field.onChange}
                        profiles={storage.data ?? []}
                        loading={storage.isPending}
                      />
                    )}
                  />
                </Field>
                <Field label={t("settings.quota")} htmlFor="ps-quota" error={errors.quota_gb?.message} hint={t("settings.quotaHint")}>
                  <Input
                    id="ps-quota"
                    inputMode="decimal"
                    placeholder={t("settings.unlimited")}
                    aria-invalid={!!errors.quota_gb}
                    {...form.register("quota_gb")}
                  />
                </Field>
              </div>
              <Field label={t("settings.origins")} htmlFor="ps-origins" hint={t("settings.originsHint")}>
                <Controller
                  control={form.control}
                  name="allowed_origins"
                  render={({ field }) => (
                    <TagInput
                      id="ps-origins"
                      value={field.value}
                      onChange={field.onChange}
                      placeholder="https://app.example.com"
                      validate={validateOrigin}
                    />
                  )}
                />
              </Field>
            </form>
          </CardContent>
          <CardFooter className="justify-end gap-2 border-t pt-6">
            <Button variant="ghost" disabled={!isDirty || update.isPending} onClick={() => form.reset(toValues(project))}>
              {t("settings.discard")}
            </Button>
            <Button type="submit" form="project-settings" disabled={!isDirty || update.isPending}>
              {update.isPending && <Loader2 className="animate-spin" />}
              {t("settings.save")}
            </Button>
          </CardFooter>
        </Card>

        <Card className={project.disabled ? "border-warning/40" : "border-destructive/25"}>
          <CardHeader>
            <CardTitle className="text-base">{project.disabled ? t("settings.disabledTitle") : t("settings.disableTitle")}</CardTitle>
            <CardDescription>
              {project.disabled
                ? t("settings.disabledDesc")
                : t("settings.disableDesc")}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <label className="flex items-center justify-between gap-4 rounded-lg border p-4">
              <span className="text-sm font-medium">{project.disabled ? t("settings.stateDisabled") : t("settings.stateEnabled")}</span>
              <Switch
                checked={!project.disabled}
                disabled={toggle.isPending}
                aria-label={t("settings.enabledAria")}
                onCheckedChange={async (enabled) => {
                  try {
                    await toggle.mutateAsync({ disabled: !enabled });
                    toast.success(enabled ? t("settings.toastEnabled") : t("settings.toastDisabled"));
                  } catch (e) {
                    toast.error(t("settings.updateFailed"), { description: errorMessage(e) });
                  }
                }}
              />
            </label>
          </CardContent>
        </Card>
      </div>

      <Card className="h-fit gap-2">
        <CardHeader>
          <CardTitle className="text-sm">{t("settings.details")}</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="mb-3">
            <div className="mb-1 text-xs text-muted-foreground">{t("settings.storageUsed")}</div>
            <UsageBar used={project.used_bytes} quota={project.quota_bytes} />
          </div>
          <dl className="divide-y">
            <KeyValue label={t("settings.environment")}>{project.environment}</KeyValue>
            <KeyValue label={t("settings.created")}>{formatDateTime(project.created_at)}</KeyValue>
            <KeyValue label={t("settings.updated")}>
              <RelativeTime value={project.updated_at} />
            </KeyValue>
          </dl>
        </CardContent>
      </Card>
    </div>
  );
}
