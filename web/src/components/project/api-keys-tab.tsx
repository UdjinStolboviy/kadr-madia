import { zodResolver } from "@hookform/resolvers/zod";
import type { MessageKey } from "@/i18n/dictionaries";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, KeyRound, Loader2, Plus, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { Controller, useForm, useWatch } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

import { CopyButton, Field, RelativeTime, StatusBadge } from "@/components/common";
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
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useT } from "@/i18n/react";
import { errorMessage, isApiError } from "@/lib/api";
import { formatDate } from "@/lib/format";
import { apiKeysQuery, useCreateApiKey, useRevokeApiKey } from "@/lib/queries";
import { API_KEY_SCOPES, type ApiKey, type ApiKeyScope, type CreateApiKeyResponse, type Project } from "@/lib/types";
import { cn } from "@/lib/utils";

const SCOPE_INFO: Record<ApiKeyScope, MessageKey> = {
  "uploads:write": "apikeys.scope.uploadsWrite",
  "assets:read": "apikeys.scope.assetsRead",
  "assets:write": "apikeys.scope.assetsWrite",
  "assets:delete": "apikeys.scope.assetsDelete",
  "urls:sign": "apikeys.scope.urlsSign",
  "namespaces:admin": "apikeys.scope.namespacesAdmin",
  "imports:write": "apikeys.scope.importsWrite",
};

const DEFAULT_SCOPES: ApiKeyScope[] = ["uploads:write", "assets:read", "urls:sign"];

const EXPIRY = [
  { value: "never", label: "apikeys.expiry.never" },
  { value: "30", label: "apikeys.expiry.30" },
  { value: "90", label: "apikeys.expiry.90" },
  { value: "365", label: "apikeys.expiry.365" },
  { value: "custom", label: "apikeys.expiry.custom" },
] as const satisfies readonly { value: string; label: MessageKey }[];

function todayPlus(days: number) {
  const d = new Date();
  d.setDate(d.getDate() + days);
  return d.toISOString().slice(0, 10);
}

const NS_RE = /^[A-Za-z0-9][A-Za-z0-9._:@-]{0,127}$/;

function parseNamespaces(raw: string): { list: string[]; valid: boolean } {
  const tokens = raw.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean);
  if (tokens.length > 100) return { list: [], valid: false };
  for (const ns of tokens) {
    if (!NS_RE.test(ns)) return { list: [], valid: false };
  }
  return { list: [...new Set(tokens)], valid: true };
}

const makeSchema = (t: (k: MessageKey) => string) => z
  .object({
    name: z.string().trim().min(1, t("apikeys.errName")).max(100),
    scopes: z.array(z.enum(API_KEY_SCOPES)).min(1, t("apikeys.errScopes")),
    namespacesText: z.string(),
    expiry: z.enum(["never", "30", "90", "365", "custom"]),
    expires_on: z.string(),
  })
  .superRefine((v, ctx) => {
    if (!parseNamespaces(v.namespacesText).valid) {
      ctx.addIssue({ code: "custom", path: ["namespacesText"], message: t("apikeys.errNamespaces") });
    }
    if (v.expiry !== "custom") return;
    if (!v.expires_on) ctx.addIssue({ code: "custom", path: ["expires_on"], message: t("apikeys.errDate") });
    else if (v.expires_on <= new Date().toISOString().slice(0, 10))
      ctx.addIssue({ code: "custom", path: ["expires_on"], message: t("apikeys.errDateFuture") });
  });
type Values = z.infer<ReturnType<typeof makeSchema>>;

function expiresAt(v: Values): string | undefined {
  if (v.expiry === "never") return undefined;
  if (v.expiry === "custom") return new Date(`${v.expires_on}T23:59:59`).toISOString();
  const d = new Date();
  d.setDate(d.getDate() + Number(v.expiry));
  return d.toISOString();
}

function keyStatus(k: ApiKey): { tone: "success" | "danger" | "neutral" | "warning"; label: MessageKey } {
  if (k.revoked_at) return { tone: "neutral", label: "apikeys.status.revoked" };
  if (k.expires_at && new Date(k.expires_at).getTime() < Date.now()) return { tone: "danger", label: "apikeys.status.expired" };
  if (k.expires_at && new Date(k.expires_at).getTime() - Date.now() < 7 * 86400_000)
    return { tone: "warning", label: "apikeys.status.expiring" };
  return { tone: "success", label: "apikeys.status.active" };
}

function SecretPanel({ result, onDone }: { result: CreateApiKeyResponse; onDone: () => void }) {
  const t = useT();
  const [copied, setCopied] = useState(false);
  return (
    <>
      <DialogHeader>
        <DialogTitle>{t("apikeys.saveTitle")}</DialogTitle>
        <DialogDescription>
          {t("apikeys.savedKeyPre")} <span className="font-medium text-foreground">{result.api_key.name}</span> {t("apikeys.savedKeyPost")}
        </DialogDescription>
      </DialogHeader>
      <div className="flex gap-3 rounded-lg border border-warning/40 bg-warning/10 p-3 text-sm">
        <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning" />
        <p>
          {t("apikeys.secretWarning")}
        </p>
      </div>
      <div className="flex items-center gap-2 rounded-lg border bg-muted/50 p-2 pl-3">
        <code className="min-w-0 flex-1 font-mono text-xs break-all select-all" data-testid="api-key-secret">
          {result.secret}
        </code>
        <CopyButton value={result.secret} variant="default" size="sm" label={t("apikeys.copy")} onCopied={() => setCopied(true)} />
      </div>
      <div className="flex flex-wrap gap-1">
        {result.api_key.scopes.map((s) => (
          <span key={s} className="rounded bg-secondary px-1.5 py-0.5 font-mono text-[11px]">
            {s}
          </span>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-1 text-xs">
        <span className="text-muted-foreground">{t("apikeys.namespaces")}:</span>
        {result.api_key.namespaces.length ? (
          result.api_key.namespaces.map((ns) => (
            <span key={ns} className="rounded border px-1.5 py-0.5 font-mono text-[11px]">
              {ns}
            </span>
          ))
        ) : (
          <span className="text-muted-foreground">{t("apikeys.namespacesAll")}</span>
        )}
      </div>
      <DialogFooter>
        <Button onClick={onDone} variant={copied ? "default" : "outline"}>
          {copied ? t("apikeys.done") : t("apikeys.savedKey")}
        </Button>
      </DialogFooter>
    </>
  );
}

function CreateKeyDialog({ project, open, onOpenChange }: { project: Project; open: boolean; onOpenChange: (o: boolean) => void }) {
  const t = useT();
  const create = useCreateApiKey(project.id);
  const schema = useMemo(() => makeSchema(t), [t]);
  const [result, setResult] = useState<CreateApiKeyResponse | null>(null);
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { name: "", scopes: DEFAULT_SCOPES, namespacesText: "", expiry: "never", expires_on: todayPlus(90) },
  });
  const { errors } = form.formState;
  const expiry = useWatch({ control: form.control, name: "expiry" });

  function close() {
    onOpenChange(false);
    setTimeout(() => {
      setResult(null);
      form.reset();
    }, 200);
  }

  async function onSubmit(v: Values) {
    try {
      const at = expiresAt(v);
      const namespaces = parseNamespaces(v.namespacesText).list;
      const res = await create.mutateAsync({
        name: v.name,
        scopes: v.scopes,
        ...(namespaces.length ? { namespaces } : {}),
        ...(at ? { expires_at: at } : {}),
      });
      setResult(res);
    } catch (e) {
      if (isApiError(e, "validation_failed") && Object.keys(e.fieldErrors).length) {
        for (const [k, msg] of Object.entries(e.fieldErrors)) {
          const field = k === "expires_at" ? "expires_on" : k === "namespaces" ? "namespacesText" : k;
          form.setError(field as keyof Values, { message: msg });
        }
      } else {
        toast.error(t("apikeys.createFailed"), { description: errorMessage(e) });
      }
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        // While the secret is shown, only explicit confirmation closes the dialog.
        if (!o && result) return;
        if (!o) close();
        else onOpenChange(o);
      }}
    >
      <DialogContent
        className="max-h-[92svh] overflow-y-auto sm:max-w-lg"
        showCloseButton={!result}
        onInteractOutside={(e) => result && e.preventDefault()}
        onEscapeKeyDown={(e) => result && e.preventDefault()}
      >
        {result ? (
          <SecretPanel result={result} onDone={close} />
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>{t("apikeys.createTitle")}</DialogTitle>
              <DialogDescription>
                {t("apikeys.createDescPre")} <span className="font-medium text-foreground">{project.name}</span>{t("apikeys.createDescPost")}
              </DialogDescription>
            </DialogHeader>
            <form id="create-key" onSubmit={form.handleSubmit(onSubmit)} noValidate className="grid gap-4">
              <Field label={t("apikeys.name")} htmlFor="key-name" error={errors.name?.message}>
                <Input
                  id="key-name"
                  placeholder="backend-production"
                  autoFocus
                  aria-invalid={!!errors.name}
                  {...form.register("name")}
                />
              </Field>
              <fieldset className="grid gap-2">
                <legend className="mb-1.5 text-sm font-medium">{t("apikeys.scopes")}</legend>
                <Controller
                  control={form.control}
                  name="scopes"
                  render={({ field }) => (
                    <div className="grid gap-1 rounded-lg border p-1">
                      {API_KEY_SCOPES.map((scope) => {
                        const checked = field.value.includes(scope);
                        const id = `scope-${scope}`;
                        return (
                          <label
                            key={scope}
                            htmlFor={id}
                            className={cn(
                              "flex cursor-pointer items-start gap-3 rounded-md px-2.5 py-2 hover:bg-accent/60",
                              checked && "bg-accent/40",
                            )}
                          >
                            <Checkbox
                              id={id}
                              checked={checked}
                              className="mt-0.5"
                              onCheckedChange={(c) =>
                                field.onChange(
                                  c ? [...field.value, scope] : field.value.filter((s: ApiKeyScope) => s !== scope),
                                )
                              }
                            />
                            <span className="grid gap-0.5">
                              <span className="font-mono text-xs font-medium">{scope}</span>
                              <span className="text-xs text-muted-foreground">{t(SCOPE_INFO[scope])}</span>
                            </span>
                          </label>
                        );
                      })}
                    </div>
                  )}
                />
                {errors.scopes && (
                  <p className="text-xs text-destructive" role="alert">
                    {errors.scopes.message}
                  </p>
                )}
              </fieldset>
              <Field
                label={t("apikeys.namespaces")}
                htmlFor="key-namespaces"
                hint={t("apikeys.namespacesHint")}
                error={errors.namespacesText?.message}
              >
                <Input
                  id="key-namespaces"
                  placeholder={t("apikeys.namespacesPlaceholder")}
                  className="font-mono text-xs"
                  aria-invalid={!!errors.namespacesText}
                  {...form.register("namespacesText")}
                />
              </Field>
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label={t("apikeys.expires")} htmlFor="key-expiry">
                  <Controller
                    control={form.control}
                    name="expiry"
                    render={({ field }) => (
                      <Select value={field.value} onValueChange={field.onChange}>
                        <SelectTrigger id="key-expiry" className="w-full">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {EXPIRY.map((o) => (
                            <SelectItem key={o.value} value={o.value}>
                              {t(o.label)}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    )}
                  />
                </Field>
                {expiry === "custom" && (
                  <Field label={t("apikeys.expiryDate")} htmlFor="key-date" error={errors.expires_on?.message}>
                    <Input
                      id="key-date"
                      type="date"
                      min={todayPlus(1)}
                      aria-invalid={!!errors.expires_on}
                      {...form.register("expires_on")}
                    />
                  </Field>
                )}
              </div>
            </form>
            <DialogFooter>
              <Button variant="outline" onClick={close}>
                {t("apikeys.cancel")}
              </Button>
              <Button type="submit" form="create-key" disabled={create.isPending}>
                {create.isPending && <Loader2 className="animate-spin" />}
                {t("apikeys.createKey")}
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}

export function ApiKeysTab({ project }: { project: Project }) {
  const t = useT();
  const keys = useQuery(apiKeysQuery(project.id));
  const revoke = useRevokeApiKey(project.id);
  const [createOpen, setCreateOpen] = useState(false);
  const [toRevoke, setToRevoke] = useState<ApiKey | null>(null);

  const list = keys.data ?? [];
  const sorted = [...list].sort((a, b) => Number(!!a.revoked_at) - Number(!!b.revoked_at) || b.created_at.localeCompare(a.created_at));

  return (
    <div className="grid gap-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="text-sm text-muted-foreground">
          {t("apikeys.intro")}{" "}
          <code className="font-mono text-xs">kd_{project.environment}_</code>.
        </p>
        <Button onClick={() => setCreateOpen(true)} disabled={project.disabled} className="shrink-0">
          <Plus /> {t("apikeys.createKey")}
        </Button>
      </div>

      {keys.isPending ? (
        <div className="space-y-2 rounded-xl border bg-card p-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : keys.isError ? (
        <ErrorState error={keys.error} onRetry={() => void keys.refetch()} />
      ) : list.length === 0 ? (
        <EmptyState
          icon={<KeyRound />}
          title={t("apikeys.emptyTitle")}
          description={t("apikeys.emptyDesc")}
          action={
            <Button onClick={() => setCreateOpen(true)} disabled={project.disabled}>
              <Plus /> {t("apikeys.createKey")}
            </Button>
          }
        />
      ) : (
        <div className="overflow-hidden rounded-xl border bg-card">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="pl-4">{t("apikeys.colName")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("apikeys.scopes")}</TableHead>
                <TableHead className="hidden sm:table-cell">{t("apikeys.colLastUsed")}</TableHead>
                <TableHead className="hidden lg:table-cell">{t("apikeys.expires")}</TableHead>
                <TableHead>{t("apikeys.colStatus")}</TableHead>
                <TableHead className="w-10" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {sorted.map((k) => {
                const st = keyStatus(k);
                return (
                  <TableRow key={k.id} className={cn(k.revoked_at && "opacity-60")}>
                    <TableCell className="pl-4">
                      <div className="font-medium">{k.name}</div>
                      <div className="font-mono text-xs text-muted-foreground">{k.prefix}…</div>
                    </TableCell>
                    <TableCell className="hidden max-w-[18rem] md:table-cell">
                      <div className="flex flex-wrap gap-1">
                        {k.scopes.map((s) => (
                          <span key={s} className="rounded bg-secondary px-1.5 py-0.5 font-mono text-[11px] whitespace-nowrap">
                            {s}
                          </span>
                        ))}
                      </div>
                      {k.namespaces.length > 0 && (
                        <div className="mt-1 flex flex-wrap items-center gap-1">
                          <span className="text-[11px] text-muted-foreground">{t("apikeys.namespaces")}:</span>
                          {k.namespaces.map((ns) => (
                            <span key={ns} className="rounded border px-1.5 py-0.5 font-mono text-[11px] whitespace-nowrap">
                              {ns}
                            </span>
                          ))}
                        </div>
                      )}
                    </TableCell>
                    <TableCell className="hidden text-sm sm:table-cell">
                      <RelativeTime value={k.last_used_at} />
                    </TableCell>
                    <TableCell className="hidden text-sm text-muted-foreground lg:table-cell">
                      {k.expires_at ? formatDate(k.expires_at) : t("apikeys.expiry.never")}
                    </TableCell>
                    <TableCell>
                      <StatusBadge tone={st.tone}>{t(st.label)}</StatusBadge>
                    </TableCell>
                    <TableCell className="pr-3">
                      {!k.revoked_at && (
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          aria-label={t("apikeys.revokeAria", { name: k.name })}
                          className="text-muted-foreground hover:text-destructive"
                          onClick={() => setToRevoke(k)}
                        >
                          <Trash2 />
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
      )}

      <CreateKeyDialog project={project} open={createOpen} onOpenChange={setCreateOpen} />

      <AlertDialog open={!!toRevoke} onOpenChange={(o) => !o && setToRevoke(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("apikeys.revokeTitle", { name: toRevoke?.name ?? "" })}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("apikeys.revokeDescPre")} <code className="font-mono text-xs">{toRevoke?.prefix}…</code> {t("apikeys.revokeDescPost")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("apikeys.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => {
                if (toRevoke) revoke.mutate(toRevoke.id);
                setToRevoke(null);
              }}
            >
              {t("apikeys.revokeKey")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
