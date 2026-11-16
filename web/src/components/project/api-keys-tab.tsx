import { zodResolver } from "@hookform/resolvers/zod";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, KeyRound, Loader2, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
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
import { errorMessage, isApiError } from "@/lib/api";
import { formatDate } from "@/lib/format";
import { apiKeysQuery, useCreateApiKey, useRevokeApiKey } from "@/lib/queries";
import { API_KEY_SCOPES, type ApiKey, type ApiKeyScope, type CreateApiKeyResponse, type Project } from "@/lib/types";
import { cn } from "@/lib/utils";

const SCOPE_INFO: Record<ApiKeyScope, string> = {
  "uploads:write": "Create upload tickets and complete uploads",
  "assets:read": "Read asset metadata and renditions",
  "assets:write": "Update asset metadata and visibility",
  "assets:delete": "Delete assets",
  "urls:sign": "Sign private delivery URLs",
  "namespaces:admin": "Manage namespaces and their quotas",
  "imports:write": "Import media from URLs and cloud drives",
};

const DEFAULT_SCOPES: ApiKeyScope[] = ["uploads:write", "assets:read", "urls:sign"];

const EXPIRY = [
  { value: "never", label: "Never" },
  { value: "30", label: "30 days" },
  { value: "90", label: "90 days" },
  { value: "365", label: "1 year" },
  { value: "custom", label: "Custom date…" },
] as const;

function todayPlus(days: number) {
  const d = new Date();
  d.setDate(d.getDate() + days);
  return d.toISOString().slice(0, 10);
}

const schema = z
  .object({
    name: z.string().trim().min(1, "Give the key a name").max(100),
    scopes: z.array(z.enum(API_KEY_SCOPES)).min(1, "Select at least one scope"),
    expiry: z.enum(["never", "30", "90", "365", "custom"]),
    expires_on: z.string(),
  })
  .superRefine((v, ctx) => {
    if (v.expiry !== "custom") return;
    if (!v.expires_on) ctx.addIssue({ code: "custom", path: ["expires_on"], message: "Pick a date" });
    else if (v.expires_on <= new Date().toISOString().slice(0, 10))
      ctx.addIssue({ code: "custom", path: ["expires_on"], message: "Date must be in the future" });
  });
type Values = z.infer<typeof schema>;

function expiresAt(v: Values): string | undefined {
  if (v.expiry === "never") return undefined;
  if (v.expiry === "custom") return new Date(`${v.expires_on}T23:59:59`).toISOString();
  const d = new Date();
  d.setDate(d.getDate() + Number(v.expiry));
  return d.toISOString();
}

function keyStatus(k: ApiKey): { tone: "success" | "danger" | "neutral" | "warning"; label: string } {
  if (k.revoked_at) return { tone: "neutral", label: "revoked" };
  if (k.expires_at && new Date(k.expires_at).getTime() < Date.now()) return { tone: "danger", label: "expired" };
  if (k.expires_at && new Date(k.expires_at).getTime() - Date.now() < 7 * 86400_000)
    return { tone: "warning", label: "expiring" };
  return { tone: "success", label: "active" };
}

function SecretPanel({ result, onDone }: { result: CreateApiKeyResponse; onDone: () => void }) {
  const [copied, setCopied] = useState(false);
  return (
    <>
      <DialogHeader>
        <DialogTitle>Save your API key</DialogTitle>
        <DialogDescription>
          Key <span className="font-medium text-foreground">{result.api_key.name}</span> was created.
        </DialogDescription>
      </DialogHeader>
      <div className="flex gap-3 rounded-lg border border-warning/40 bg-warning/10 p-3 text-sm">
        <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning" />
        <p>
          This is the <strong>only time</strong> the secret is shown. Copy it now and store it in your app's secret
          manager — Kadr keeps only a hash and cannot show it again.
        </p>
      </div>
      <div className="flex items-center gap-2 rounded-lg border bg-muted/50 p-2 pl-3">
        <code className="min-w-0 flex-1 font-mono text-xs break-all select-all" data-testid="api-key-secret">
          {result.secret}
        </code>
        <CopyButton value={result.secret} variant="default" size="sm" label="Copy" onCopied={() => setCopied(true)} />
      </div>
      <div className="flex flex-wrap gap-1">
        {result.api_key.scopes.map((s) => (
          <span key={s} className="rounded bg-secondary px-1.5 py-0.5 font-mono text-[11px]">
            {s}
          </span>
        ))}
      </div>
      <DialogFooter>
        <Button onClick={onDone} variant={copied ? "default" : "outline"}>
          {copied ? "Done" : "I've saved the key"}
        </Button>
      </DialogFooter>
    </>
  );
}

function CreateKeyDialog({ project, open, onOpenChange }: { project: Project; open: boolean; onOpenChange: (o: boolean) => void }) {
  const create = useCreateApiKey(project.id);
  const [result, setResult] = useState<CreateApiKeyResponse | null>(null);
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { name: "", scopes: DEFAULT_SCOPES, expiry: "never", expires_on: todayPlus(90) },
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
      const res = await create.mutateAsync({ name: v.name, scopes: v.scopes, ...(at ? { expires_at: at } : {}) });
      setResult(res);
    } catch (e) {
      if (isApiError(e, "validation_failed") && Object.keys(e.fieldErrors).length) {
        for (const [k, msg] of Object.entries(e.fieldErrors)) {
          form.setError((k === "expires_at" ? "expires_on" : k) as keyof Values, { message: msg });
        }
      } else {
        toast.error("Couldn't create API key", { description: errorMessage(e) });
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
              <DialogTitle>Create API key</DialogTitle>
              <DialogDescription>
                For server-to-server calls from <span className="font-medium text-foreground">{project.name}</span>'s
                backend. Never ship it to browsers or mobile apps.
              </DialogDescription>
            </DialogHeader>
            <form id="create-key" onSubmit={form.handleSubmit(onSubmit)} noValidate className="grid gap-4">
              <Field label="Name" htmlFor="key-name" error={errors.name?.message}>
                <Input
                  id="key-name"
                  placeholder="backend-production"
                  autoFocus
                  aria-invalid={!!errors.name}
                  {...form.register("name")}
                />
              </Field>
              <fieldset className="grid gap-2">
                <legend className="mb-1.5 text-sm font-medium">Scopes</legend>
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
                              <span className="text-xs text-muted-foreground">{SCOPE_INFO[scope]}</span>
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
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label="Expires" htmlFor="key-expiry">
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
                              {o.label}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    )}
                  />
                </Field>
                {expiry === "custom" && (
                  <Field label="Expiry date" htmlFor="key-date" error={errors.expires_on?.message}>
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
                Cancel
              </Button>
              <Button type="submit" form="create-key" disabled={create.isPending}>
                {create.isPending && <Loader2 className="animate-spin" />}
                Create key
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}

export function ApiKeysTab({ project }: { project: Project }) {
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
          Secret keys authenticate your backend against the Kadr API. Keys start with{" "}
          <code className="font-mono text-xs">kd_{project.environment}_</code>.
        </p>
        <Button onClick={() => setCreateOpen(true)} disabled={project.disabled} className="shrink-0">
          <Plus /> Create key
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
          title="No API keys"
          description="Create a key to let your backend issue upload tickets and sign URLs."
          action={
            <Button onClick={() => setCreateOpen(true)} disabled={project.disabled}>
              <Plus /> Create key
            </Button>
          }
        />
      ) : (
        <div className="overflow-hidden rounded-xl border bg-card">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="pl-4">Name</TableHead>
                <TableHead className="hidden md:table-cell">Scopes</TableHead>
                <TableHead className="hidden sm:table-cell">Last used</TableHead>
                <TableHead className="hidden lg:table-cell">Expires</TableHead>
                <TableHead>Status</TableHead>
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
                    </TableCell>
                    <TableCell className="hidden text-sm sm:table-cell">
                      <RelativeTime value={k.last_used_at} />
                    </TableCell>
                    <TableCell className="hidden text-sm text-muted-foreground lg:table-cell">
                      {k.expires_at ? formatDate(k.expires_at) : "Never"}
                    </TableCell>
                    <TableCell>
                      <StatusBadge tone={st.tone}>{st.label}</StatusBadge>
                    </TableCell>
                    <TableCell className="pr-3">
                      {!k.revoked_at && (
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          aria-label={`Revoke ${k.name}`}
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
            <AlertDialogTitle>Revoke “{toRevoke?.name}”?</AlertDialogTitle>
            <AlertDialogDescription>
              Requests using <code className="font-mono text-xs">{toRevoke?.prefix}…</code> will be rejected
              immediately. This can't be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => {
                if (toRevoke) revoke.mutate(toRevoke.id);
                setToRevoke(null);
              }}
            >
              Revoke key
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
