import { zodResolver } from "@hookform/resolvers/zod";
import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ChevronRight, FolderKanban, Loader2, Plus } from "lucide-react";
import { Controller, useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

import { Field, PageHeader, RelativeTime, StatusBadge } from "@/components/common";
import { EnvBadge, StorageSelect, UsageBar } from "@/components/project-form";
import { NO_STORAGE, validateOrigin } from "@/lib/forms";
import { useT } from "@/i18n/react";
import type { Translator } from "@/i18n/core";
import type { MessageKey } from "@/i18n/dictionaries";
import { EmptyState, ErrorState } from "@/components/states";
import { TagInput } from "@/components/tag-input";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { errorMessage, isApiError } from "@/lib/api";
import { gbToBytes } from "@/lib/format";
import { projectsQuery, storageQuery, useCreateProject } from "@/lib/queries";
import { SLUG_RE, type Project, type StorageProfile } from "@/lib/types";
import { cn } from "@/lib/utils";

const route = getRouteApi("/app/projects");

function makeCreateSchema(t: Translator<MessageKey>) {
  return z.object({
    slug: z.string().trim().regex(SLUG_RE, t("projects.create.slugInvalid")),
    name: z.string().trim().min(1, t("projects.create.nameRequired")).max(100),
    environment: z.enum(["live", "test"]),
    storage_profile_id: z.string(),
    quota_gb: z
      .string()
      .trim()
      .refine((v) => v === "" || (/^\d+(\.\d+)?$/.test(v) && Number(v) > 0), t("projects.create.quotaInvalid")),
    allowed_origins: z.array(z.string()),
  });
}
type CreateValues = z.infer<ReturnType<typeof makeCreateSchema>>;

function slugify(name: string) {
  return name
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^[^a-z]+/, "")
    .replace(/-+$/, "")
    .slice(0, 32);
}

function NewProjectDialog({
  open,
  onOpenChange,
  profiles,
  profilesLoading,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  profiles: StorageProfile[];
  profilesLoading: boolean;
}) {
  const t = useT();
  const navigate = useNavigate();
  const create = useCreateProject();
  const form = useForm<CreateValues>({
    resolver: zodResolver(makeCreateSchema(t)),
    defaultValues: {
      slug: "",
      name: "",
      environment: "live",
      storage_profile_id: profiles.find((p) => p.is_platform)?.id ?? profiles[0]?.id ?? NO_STORAGE,
      quota_gb: "",
      allowed_origins: [],
    },
  });
  const { errors } = form.formState;

  async function onSubmit(v: CreateValues) {
    try {
      const project = await create.mutateAsync({
        slug: v.slug,
        name: v.name,
        environment: v.environment,
        ...(v.storage_profile_id !== NO_STORAGE ? { storage_profile_id: v.storage_profile_id } : {}),
        ...(v.quota_gb ? { quota_bytes: gbToBytes(Number(v.quota_gb)) } : {}),
        ...(v.allowed_origins.length ? { allowed_origins: v.allowed_origins } : {}),
      });
      toast.success(t("projects.create.created", { name: project.name }), { description: t("projects.create.createdDescription") });
      onOpenChange(false);
      form.reset();
      void navigate({ to: "/projects/$projectId", params: { projectId: project.id } });
    } catch (e) {
      if (isApiError(e, "slug_taken")) {
        form.setError("slug", { message: t("projects.create.slugTaken") });
      } else if (isApiError(e, "validation_failed") && Object.keys(e.fieldErrors).length) {
        for (const [k, msg] of Object.entries(e.fieldErrors)) {
          const field = (k === "quota_bytes" ? "quota_gb" : k) as keyof CreateValues;
          form.setError(field, { message: msg });
        }
      } else {
        toast.error(t("projects.create.failed"), { description: errorMessage(e) });
      }
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        onOpenChange(o);
        if (!o) form.reset();
      }}
    >
      <DialogContent className="max-h-[92svh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("projects.create.title")}</DialogTitle>
          <DialogDescription>{t("projects.create.description")}</DialogDescription>
        </DialogHeader>
        <form id="new-project" onSubmit={form.handleSubmit(onSubmit)} noValidate className="grid gap-4">
          <Field label={t("projects.create.name")} htmlFor="np-name" error={errors.name?.message}>
            <Input
              id="np-name"
              placeholder={t("projects.create.namePlaceholder")}
              autoFocus
              aria-invalid={!!errors.name}
              {...form.register("name", {
                onChange: (e: React.ChangeEvent<HTMLInputElement>) => {
                  if (!form.getFieldState("slug").isDirty) {
                    form.setValue("slug", slugify(e.target.value), { shouldValidate: form.formState.isSubmitted });
                  }
                },
              })}
            />
          </Field>
          <Field
            label={t("projects.create.slug")}
            htmlFor="np-slug"
            error={errors.slug?.message}
            hint={t("projects.create.slugHint")}
          >
            <Input
              id="np-slug"
              placeholder="acme-staging"
              className="font-mono"
              autoComplete="off"
              spellCheck={false}
              aria-invalid={!!errors.slug}
              {...form.register("slug")}
            />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("projects.create.environment")} error={errors.environment?.message}>
              <Controller
                control={form.control}
                name="environment"
                render={({ field }) => (
                  <div role="radiogroup" aria-label={t("projects.create.environment")} className="grid grid-cols-2 gap-1 rounded-md bg-muted p-1">
                    {(["live", "test"] as const).map((env) => (
                      <button
                        key={env}
                        type="button"
                        role="radio"
                        aria-checked={field.value === env}
                        onClick={() => field.onChange(env)}
                        className={cn(
                          "h-7 rounded-sm text-sm capitalize outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
                          field.value === env ? "bg-background font-medium shadow-sm" : "text-muted-foreground",
                        )}
                      >
                        {t(env === "live" ? "projects.env.live" : "projects.env.test")}
                      </button>
                    ))}
                  </div>
                )}
              />
            </Field>
            <Field label={t("projects.create.quota")} htmlFor="np-quota" error={errors.quota_gb?.message} hint={t("projects.create.quotaHint")}>
              <Input
                id="np-quota"
                inputMode="decimal"
                placeholder={t("projects.create.quotaPlaceholder")}
                aria-invalid={!!errors.quota_gb}
                {...form.register("quota_gb")}
              />
            </Field>
          </div>
          <Field label={t("projects.create.storage")} htmlFor="np-storage" error={errors.storage_profile_id?.message}>
            <Controller
              control={form.control}
              name="storage_profile_id"
              render={({ field }) => (
                <StorageSelect
                  id="np-storage"
                  value={field.value}
                  onChange={field.onChange}
                  profiles={profiles}
                  loading={profilesLoading}
                />
              )}
            />
          </Field>
          <Field
            label={t("projects.create.origins")}
            htmlFor="np-origins"
            hint={t("projects.create.originsHint")}
            error={errors.allowed_origins?.message}
          >
            <Controller
              control={form.control}
              name="allowed_origins"
              render={({ field }) => (
                <TagInput
                  id="np-origins"
                  value={field.value}
                  onChange={field.onChange}
                  placeholder="https://app.example.com"
                  validate={validateOrigin}
                />
              )}
            />
          </Field>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("projects.create.cancel")}
          </Button>
          <Button type="submit" form="new-project" disabled={create.isPending}>
            {create.isPending && <Loader2 className="animate-spin" />}
            {t("projects.create.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ProjectsTable({ projects, profiles }: { projects: Project[]; profiles: StorageProfile[] }) {
  const t = useT();
  const navigate = useNavigate();
  const byId = new Map(profiles.map((p) => [p.id, p.name]));
  return (
    <div className="overflow-hidden rounded-xl border bg-card">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="pl-4">{t("projects.table.project")}</TableHead>
            <TableHead>{t("projects.table.environment")}</TableHead>
            <TableHead className="hidden md:table-cell">{t("projects.table.storage")}</TableHead>
            <TableHead className="hidden sm:table-cell">{t("projects.table.usage")}</TableHead>
            <TableHead className="hidden lg:table-cell">{t("projects.table.created")}</TableHead>
            <TableHead className="w-8" />
          </TableRow>
        </TableHeader>
        <TableBody>
          {projects.map((p) => (
            <TableRow
              key={p.id}
              className="cursor-pointer"
              onClick={() => void navigate({ to: "/projects/$projectId", params: { projectId: p.id } })}
            >
              <TableCell className="pl-4">
                <Link
                  to="/projects/$projectId"
                  params={{ projectId: p.id }}
                  className="block rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                  onClick={(e) => e.stopPropagation()}
                >
                  <div className="flex items-center gap-2 font-medium">
                    {p.name}
                    {p.disabled && <StatusBadge tone="neutral">{t("projects.disabled")}</StatusBadge>}
                  </div>
                  <div className="font-mono text-xs text-muted-foreground">{p.slug}</div>
                </Link>
              </TableCell>
              <TableCell>
                <EnvBadge env={p.environment} />
              </TableCell>
              <TableCell className="hidden text-sm text-muted-foreground md:table-cell">
                {p.storage_profile_id ? (byId.get(p.storage_profile_id) ?? t("projects.unknown")) : t("projects.platformDefault")}
              </TableCell>
              <TableCell className="hidden sm:table-cell">
                <UsageBar used={p.used_bytes} quota={p.quota_bytes} />
              </TableCell>
              <TableCell className="hidden text-sm text-muted-foreground lg:table-cell">
                <RelativeTime value={p.created_at} />
              </TableCell>
              <TableCell className="pr-4 text-muted-foreground">
                <ChevronRight className="size-4" />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

export default function ProjectsPage() {
  const t = useT();
  const search = route.useSearch();
  const navigate = route.useNavigate();
  const projects = useQuery(projectsQuery);
  const storage = useQuery(storageQuery);
  const open = !!search.new;
  const setOpen = (o: boolean) => void navigate({ search: o ? { new: true } : {}, replace: true });

  return (
    <>
      <PageHeader
        title={t("projects.title")}
        description={t("projects.description")}
        actions={
          <Button onClick={() => setOpen(true)}>
            <Plus /> {t("projects.new")}
          </Button>
        }
      />
      {projects.isPending ? (
        <div className="space-y-2 rounded-xl border bg-card p-4">
          {Array.from({ length: 4 }, (_, i) => (
            <Skeleton key={i} className="h-12 w-full" />
          ))}
        </div>
      ) : projects.isError ? (
        <ErrorState error={projects.error} onRetry={() => void projects.refetch()} />
      ) : projects.data.length === 0 ? (
        <EmptyState
          icon={<FolderKanban />}
          title={t("projects.empty.title")}
          description={
            storage.data?.length === 0
              ? t("projects.empty.descriptionNoStorage")
              : t("projects.empty.description")
          }
          action={
            storage.data?.length === 0 ? (
              <Button asChild variant="outline">
                <Link to="/storage" search={{ new: true }}>
                  {t("projects.empty.addStorage")}
                </Link>
              </Button>
            ) : (
              <Button onClick={() => setOpen(true)}>
                <Plus /> {t("projects.new")}
              </Button>
            )
          }
        />
      ) : (
        <ProjectsTable projects={projects.data} profiles={storage.data ?? []} />
      )}
      {open && (
        <NewProjectDialog
          open={open}
          onOpenChange={setOpen}
          profiles={storage.data ?? []}
          profilesLoading={storage.isPending}
        />
      )}
    </>
  );
}
