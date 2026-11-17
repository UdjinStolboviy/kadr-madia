import { zodResolver } from "@hookform/resolvers/zod";
import { Eye, EyeOff, Loader2 } from "lucide-react";
import { useState } from "react";
import { Controller, useForm, useWatch } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

import { Field } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useT } from "@/i18n/react";
import type { Translator } from "@/i18n/core";
import type { MessageKey } from "@/i18n/dictionaries";
import { errorMessage, isApiError } from "@/lib/api";
import { PROVIDER_LABEL } from "@/lib/forms";
import { useSaveStorageProfile } from "@/lib/queries";
import { STORAGE_PROVIDERS, type StorageProfile, type StorageProfileInput, type StorageProvider } from "@/lib/types";

const PROVIDER_DEFAULTS: Record<StorageProvider, { endpoint: string; region: string }> = {
  r2: { endpoint: "https://<ACCOUNT_ID>.r2.cloudflarestorage.com", region: "auto" },
  s3: { endpoint: "https://s3.<REGION>.amazonaws.com", region: "us-east-1" },
  b2: { endpoint: "https://s3.<REGION>.backblazeb2.com", region: "eu-central-003" },
  hetzner: { endpoint: "https://<LOCATION>.your-objectstorage.com", region: "fsn1" },
  garage: { endpoint: "http://garage.internal:3900", region: "garage" },
};

const url = (msg: string) =>
  z
    .string()
    .trim()
    .refine((v) => {
      try {
        const u = new URL(v);
        return u.protocol === "https:" || u.protocol === "http:";
      } catch {
        return false;
      }
    }, msg);

function makeSchema(editing: boolean, t: Translator<MessageKey>) {
  const bucket = z
    .string()
    .trim()
    .regex(/^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$/, t("storage.form.bucketInvalid"));
  const credential = (label: string) =>
    editing ? z.string().trim() : z.string().trim().min(1, t("storage.form.required", { label }));
  return z
    .object({
      name: z.string().trim().min(1, t("storage.form.nameRequired")).max(100),
      provider: z.enum(STORAGE_PROVIDERS),
      endpoint: url(t("storage.form.endpointInvalid")).refine((v) => !v.includes("<"), t("storage.form.endpointPlaceholder")),
      region: z.string().trim().min(1, t("storage.form.regionRequired")),
      public_bucket: bucket,
      private_bucket: bucket,
      public_base_url: url(t("storage.form.publicBaseUrlInvalid")),
      private_base_url: z.union([z.literal(""), url(t("storage.form.urlInvalid"))]),
      access_key_id: credential(t("storage.form.accessKeyId")),
      secret_access_key: credential(t("storage.form.secretKey")),
      is_platform: z.boolean(),
    })
    .refine((v) => v.public_bucket !== v.private_bucket, {
      path: ["private_bucket"],
      message: t("storage.form.bucketsDiffer"),
    });
}
type Values = z.infer<ReturnType<typeof makeSchema>>;

function defaults(p?: StorageProfile): Values {
  return {
    name: p?.name ?? "",
    provider: p?.provider ?? "r2",
    endpoint: p?.endpoint ?? "",
    region: p?.region ?? "auto",
    public_bucket: p?.public_bucket ?? "",
    private_bucket: p?.private_bucket ?? "",
    public_base_url: p?.public_base_url ?? "",
    private_base_url: p?.private_base_url ?? "",
    access_key_id: "",
    secret_access_key: "",
    is_platform: p?.is_platform ?? false,
  };
}

export function StorageFormDialog({
  open,
  onOpenChange,
  profile,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  profile?: StorageProfile;
}) {
  const t = useT();
  const editing = !!profile;
  const save = useSaveStorageProfile();
  const [showSecret, setShowSecret] = useState(false);
  const form = useForm<Values>({ resolver: zodResolver(makeSchema(editing, t)), defaultValues: defaults(profile) });
  const { errors } = form.formState;
  const provider = useWatch({ control: form.control, name: "provider" });
  const hint = PROVIDER_DEFAULTS[provider];

  async function onSubmit(v: Values) {
    try {
      if (profile) {
        // PATCH only what changed; credentials only when a new value was typed.
        const dirty = form.formState.dirtyFields;
        const patch: Partial<StorageProfileInput> = {};
        const keys = [
          "name",
          "provider",
          "endpoint",
          "region",
          "public_bucket",
          "private_bucket",
          "public_base_url",
          "private_base_url",
          "is_platform",
        ] as const;
        for (const k of keys) {
          if (dirty[k]) (patch as Record<string, unknown>)[k] = v[k];
        }
        if (v.access_key_id) patch.access_key_id = v.access_key_id;
        if (v.secret_access_key) patch.secret_access_key = v.secret_access_key;
        if (!Object.keys(patch).length) {
          onOpenChange(false);
          return;
        }
        await save.mutateAsync({ id: profile.id, patch });
        toast.success(t("storage.form.updated"));
      } else {
        const { private_base_url, ...rest } = v;
        await save.mutateAsync({ create: { ...rest, ...(private_base_url ? { private_base_url } : {}) } });
        toast.success(t("storage.form.created", { name: v.name }), { description: t("storage.form.createdDescription") });
      }
      onOpenChange(false);
    } catch (e) {
      if (isApiError(e, "validation_failed") && Object.keys(e.fieldErrors).length) {
        for (const [k, msg] of Object.entries(e.fieldErrors)) form.setError(k as keyof Values, { message: msg });
      } else {
        toast.error(editing ? t("storage.form.updateFailed") : t("storage.form.createFailed"), { description: errorMessage(e) });
      }
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[92svh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{editing ? t("storage.form.titleEdit", { name: profile.name }) : t("storage.form.titleAdd")}</DialogTitle>
          <DialogDescription>
            {t("storage.form.description")}
          </DialogDescription>
        </DialogHeader>
        <form id="storage-form" onSubmit={form.handleSubmit(onSubmit)} noValidate className="grid gap-5">
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("storage.form.name")} htmlFor="sp-name" error={errors.name?.message}>
              <Input id="sp-name" placeholder="R2 production" autoFocus aria-invalid={!!errors.name} {...form.register("name")} />
            </Field>
            <Field label={t("storage.form.provider")} htmlFor="sp-provider">
              <Controller
                control={form.control}
                name="provider"
                render={({ field }) => (
                  <Select
                    value={field.value}
                    onValueChange={(v) => {
                      const p = v as StorageProvider;
                      field.onChange(p);
                      const cur = form.getValues("region");
                      const wasDefault = Object.values(PROVIDER_DEFAULTS).some((d) => d.region === cur) || !cur;
                      if (wasDefault) form.setValue("region", PROVIDER_DEFAULTS[p].region, { shouldDirty: true });
                    }}
                  >
                    <SelectTrigger id="sp-provider" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {STORAGE_PROVIDERS.map((p) => (
                        <SelectItem key={p} value={p}>
                          {PROVIDER_LABEL[p]}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              />
            </Field>
          </div>

          <div className="grid gap-4 sm:grid-cols-[1fr_9rem]">
            <Field
              label={t("storage.form.endpoint")}
              htmlFor="sp-endpoint"
              error={errors.endpoint?.message}
              hint={provider === "r2" ? t("storage.form.endpointHintR2") : undefined}
            >
              <Input
                id="sp-endpoint"
                placeholder={hint.endpoint}
                className="font-mono text-xs"
                spellCheck={false}
                aria-invalid={!!errors.endpoint}
                {...form.register("endpoint")}
              />
            </Field>
            <Field label={t("storage.form.region")} htmlFor="sp-region" error={errors.region?.message}>
              <Input
                id="sp-region"
                placeholder={hint.region}
                className="font-mono text-xs"
                aria-invalid={!!errors.region}
                {...form.register("region")}
              />
            </Field>
          </div>

          <fieldset className="grid gap-4 rounded-lg border p-4">
            <legend className="px-1 text-sm font-medium">{t("storage.form.buckets")}</legend>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={t("storage.form.publicBucket")} htmlFor="sp-pub" error={errors.public_bucket?.message}>
                <Input
                  id="sp-pub"
                  placeholder="acme-public"
                  className="font-mono text-xs"
                  aria-invalid={!!errors.public_bucket}
                  {...form.register("public_bucket")}
                />
              </Field>
              <Field
                label={t("storage.form.publicBaseUrl")}
                htmlFor="sp-pub-url"
                error={errors.public_base_url?.message}
                hint={t("storage.form.publicBaseUrlHint")}
              >
                <Input
                  id="sp-pub-url"
                  placeholder="https://cdn.example.com"
                  className="font-mono text-xs"
                  aria-invalid={!!errors.public_base_url}
                  {...form.register("public_base_url")}
                />
              </Field>
              <Field label={t("storage.form.privateBucket")} htmlFor="sp-priv" error={errors.private_bucket?.message}>
                <Input
                  id="sp-priv"
                  placeholder="acme-private"
                  className="font-mono text-xs"
                  aria-invalid={!!errors.private_bucket}
                  {...form.register("private_bucket")}
                />
              </Field>
              <Field
                label={
                  <>
                    {t("storage.form.privateBaseUrl")} <span className="font-normal text-muted-foreground">{t("storage.form.optional")}</span>
                  </>
                }
                htmlFor="sp-priv-url"
                error={errors.private_base_url?.message}
                hint={t("storage.form.privateBaseUrlHint")}
              >
                <Input
                  id="sp-priv-url"
                  placeholder="https://media.example.com"
                  className="font-mono text-xs"
                  aria-invalid={!!errors.private_base_url}
                  {...form.register("private_base_url")}
                />
              </Field>
            </div>
          </fieldset>

          <fieldset className="grid gap-4 rounded-lg border p-4">
            <legend className="px-1 text-sm font-medium">{t("storage.form.credentials")}</legend>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                label={t("storage.form.accessKeyId")}
                htmlFor="sp-akid"
                error={errors.access_key_id?.message}
                hint={editing ? t("storage.form.accessKeyIdHintEdit", { id: profile.access_key_id }) : undefined}
              >
                <Input
                  id="sp-akid"
                  autoComplete="off"
                  spellCheck={false}
                  className="font-mono text-xs"
                  placeholder={editing ? t("storage.form.unchanged") : ""}
                  aria-invalid={!!errors.access_key_id}
                  {...form.register("access_key_id")}
                />
              </Field>
              <Field
                label={t("storage.form.secretKey")}
                htmlFor="sp-secret"
                error={errors.secret_access_key?.message}
                hint={editing ? t("storage.form.secretKeyHintEdit") : t("storage.form.secretKeyHintNew")}
              >
                <div className="relative">
                  <Input
                    id="sp-secret"
                    type={showSecret ? "text" : "password"}
                    autoComplete="new-password"
                    spellCheck={false}
                    className="pr-9 font-mono text-xs"
                    placeholder={editing ? t("storage.form.secretUnchanged") : ""}
                    aria-invalid={!!errors.secret_access_key}
                    {...form.register("secret_access_key")}
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-xs"
                    className="absolute top-1/2 right-1.5 -translate-y-1/2 text-muted-foreground"
                    aria-label={showSecret ? t("storage.form.hideSecret") : t("storage.form.showSecret")}
                    onClick={() => setShowSecret((s) => !s)}
                  >
                    {showSecret ? <EyeOff /> : <Eye />}
                  </Button>
                </div>
              </Field>
            </div>
          </fieldset>

          <Controller
            control={form.control}
            name="is_platform"
            render={({ field }) => (
              <label htmlFor="sp-platform" className="flex cursor-pointer items-start gap-3 rounded-lg border p-4">
                <Checkbox
                  id="sp-platform"
                  checked={field.value}
                  onCheckedChange={(c) => field.onChange(c === true)}
                  className="mt-0.5"
                />
                <span className="grid gap-0.5">
                  <span className="text-sm font-medium">{t("storage.form.platform")}</span>
                  <span className="text-xs text-muted-foreground">
                    {t("storage.form.platformHint")}
                  </span>
                </span>
              </label>
            )}
          />
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("storage.form.cancel")}
          </Button>
          <Button type="submit" form="storage-form" disabled={save.isPending}>
            {save.isPending && <Loader2 className="animate-spin" />}
            {editing ? t("storage.form.saveChanges") : t("storage.form.titleAdd")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
