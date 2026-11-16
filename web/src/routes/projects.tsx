import { zodResolver } from "@hookform/resolvers/zod";
import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ChevronRight, FolderKanban, Loader2, Plus } from "lucide-react";
import { Controller, useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

import { Field, PageHeader, RelativeTime, StatusBadge } from "@/components/common";
import { EnvBadge, StorageSelect, UsageBar } from "@/components/project-form";
import { NO_STORAGE, quotaSchema, validateOrigin } from "@/lib/forms";
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

const createSchema = z.object({
  slug: z
    .string()
    .trim()
    .regex(SLUG_RE, "2–32 chars: lowercase letters, digits and dashes; must start with a letter"),
  name: z.string().trim().min(1, "Name is required").max(100),
  environment: z.enum(["live", "test"]),
  storage_profile_id: z.string(),
  quota_gb: quotaSchema,
  allowed_origins: z.array(z.string()),
});
type CreateValues = z.infer<typeof createSchema>;

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
  const navigate = useNavigate();
  const create = useCreateProject();
  const form = useForm<CreateValues>({
    resolver: zodResolver(createSchema),
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
      toast.success(`Project “${project.name}” created`, { description: "Default presets were added." });
      onOpenChange(false);
      form.reset();
      void navigate({ to: "/projects/$projectId", params: { projectId: project.id } });
    } catch (e) {
      if (isApiError(e, "slug_taken")) {
        form.setError("slug", { message: "This slug is already taken" });
      } else if (isApiError(e, "validation_failed") && Object.keys(e.fieldErrors).length) {
        for (const [k, msg] of Object.entries(e.fieldErrors)) {
          const field = (k === "quota_bytes" ? "quota_gb" : k) as keyof CreateValues;
          form.setError(field, { message: msg });
        }
      } else {
        toast.error("Couldn't create project", { description: errorMessage(e) });
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
          <DialogTitle>New project</DialogTitle>
          <DialogDescription>A project isolates assets, presets, quotas and API keys for one app.</DialogDescription>
        </DialogHeader>
        <form id="new-project" onSubmit={form.handleSubmit(onSubmit)} noValidate className="grid gap-4">
          <Field label="Name" htmlFor="np-name" error={errors.name?.message}>
            <Input
              id="np-name"
              placeholder="Acme staging"
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
            label="Slug"
            htmlFor="np-slug"
            error={errors.slug?.message}
            hint="Used in storage keys and URLs. Can't be changed later."
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
            <Field label="Environment" error={errors.environment?.message}>
              <Controller
                control={form.control}
                name="environment"
                render={({ field }) => (
                  <div role="radiogroup" aria-label="Environment" className="grid grid-cols-2 gap-1 rounded-md bg-muted p-1">
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
                        {env}
                      </button>
                    ))}
                  </div>
                )}
              />
            </Field>
            <Field label="Quota (GB)" htmlFor="np-quota" error={errors.quota_gb?.message} hint="Empty = unlimited">
              <Input
                id="np-quota"
                inputMode="decimal"
                placeholder="Unlimited"
                aria-invalid={!!errors.quota_gb}
                {...form.register("quota_gb")}
              />
            </Field>
          </div>
          <Field label="Storage profile" htmlFor="np-storage" error={errors.storage_profile_id?.message}>
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
            label="Allowed origins"
            htmlFor="np-origins"
            hint="Browser origins allowed to upload directly. Press Enter to add."
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
            Cancel
          </Button>
          <Button type="submit" form="new-project" disabled={create.isPending}>
            {create.isPending && <Loader2 className="animate-spin" />}
            Create project
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ProjectsTable({ projects, profiles }: { projects: Project[]; profiles: StorageProfile[] }) {
  const navigate = useNavigate();
  const byId = new Map(profiles.map((p) => [p.id, p.name]));
  return (
    <div className="overflow-hidden rounded-xl border bg-card">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="pl-4">Project</TableHead>
            <TableHead>Environment</TableHead>
            <TableHead className="hidden md:table-cell">Storage</TableHead>
            <TableHead className="hidden sm:table-cell">Usage</TableHead>
            <TableHead className="hidden lg:table-cell">Created</TableHead>
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
                    {p.disabled && <StatusBadge tone="neutral">disabled</StatusBadge>}
                  </div>
                  <div className="font-mono text-xs text-muted-foreground">{p.slug}</div>
                </Link>
              </TableCell>
              <TableCell>
                <EnvBadge env={p.environment} />
              </TableCell>
              <TableCell className="hidden text-sm text-muted-foreground md:table-cell">
                {p.storage_profile_id ? (byId.get(p.storage_profile_id) ?? "Unknown") : "Platform default"}
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
  const search = route.useSearch();
  const navigate = route.useNavigate();
  const projects = useQuery(projectsQuery);
  const storage = useQuery(storageQuery);
  const open = !!search.new;
  const setOpen = (o: boolean) => void navigate({ search: o ? { new: true } : {}, replace: true });

  return (
    <>
      <PageHeader
        title="Projects"
        description="Apps that upload and serve media through Kadr."
        actions={
          <Button onClick={() => setOpen(true)}>
            <Plus /> New project
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
          title="No projects yet"
          description={
            storage.data?.length === 0
              ? "Add a storage profile first, then create a project for each app that stores media."
              : "Create a project for each app that stores media. Default presets are created automatically."
          }
          action={
            storage.data?.length === 0 ? (
              <Button asChild variant="outline">
                <Link to="/storage" search={{ new: true }}>
                  Add storage
                </Link>
              </Button>
            ) : (
              <Button onClick={() => setOpen(true)}>
                <Plus /> New project
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
