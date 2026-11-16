import { useQuery } from "@tanstack/react-query";
import { getRouteApi } from "@tanstack/react-router";
import {
  CheckCircle2,
  Globe,
  HardDrive,
  Loader2,
  Lock,
  MoreHorizontal,
  Pencil,
  Plus,
  ShieldCheck,
  Trash2,
  XCircle,
  Zap,
} from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import { PageHeader, RelativeTime, StatusBadge, TestStatusBadge } from "@/components/common";
import { EmptyState, ErrorState } from "@/components/states";
import { StorageFormDialog } from "@/components/storage-form";
import { PROVIDER_LABEL } from "@/lib/forms";
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
import { Card, CardContent, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import { errorMessage, isApiError } from "@/lib/api";
import { formatMs } from "@/lib/format";
import { storageQuery, useDeleteStorageProfile, useTestStorageProfile } from "@/lib/queries";
import type { StorageProfile, StorageTestResult } from "@/lib/types";
import { cn } from "@/lib/utils";

const route = getRouteApi("/app/storage");

const CHECK_LABEL: Record<string, string> = {
  private_put: "Upload to private bucket",
  private_head: "Read metadata (HEAD)",
  private_delete: "Delete from private bucket",
  private_presign_put: "Presigned PUT upload",
  public_put: "Upload to public bucket",
  public_get_via_base_url: "Fetch via public base URL",
  public_delete: "Delete from public bucket",
};

function TestDialog({
  profile,
  result,
  pending,
  error,
  onRetry,
  onClose,
}: {
  profile: StorageProfile | null;
  result: StorageTestResult | undefined;
  pending: boolean;
  error: unknown;
  onRetry: () => void;
  onClose: () => void;
}) {
  const total = result?.checks.reduce((s, c) => s + c.duration_ms, 0) ?? 0;
  return (
    <Dialog open={!!profile} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[92svh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Connection test</DialogTitle>
          <DialogDescription>
            {profile?.name} · {profile ? PROVIDER_LABEL[profile.provider] : ""}
          </DialogDescription>
        </DialogHeader>

        {pending ? (
          <div className="grid gap-2" aria-live="polite" aria-busy="true">
            <p className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" /> Running checks against both buckets…
            </p>
            {Array.from({ length: 7 }, (_, i) => (
              <Skeleton key={i} className="h-11 w-full" />
            ))}
          </div>
        ) : error ? (
          <ErrorState title="Test could not run" error={error} />
        ) : result ? (
          <div className="grid gap-3" aria-live="polite">
            <div
              className={cn(
                "flex items-center gap-3 rounded-lg border p-3",
                result.ok ? "border-success/30 bg-success/10" : "border-destructive/30 bg-destructive/10",
              )}
            >
              {result.ok ? (
                <CheckCircle2 className="size-5 text-success" />
              ) : (
                <XCircle className="size-5 text-destructive" />
              )}
              <div className="text-sm">
                <div className="font-medium">{result.ok ? "All checks passed" : "Some checks failed"}</div>
                <div className="text-muted-foreground">
                  {result.checks.filter((c) => c.ok).length}/{result.checks.length} passed · {formatMs(total)}
                </div>
              </div>
            </div>
            <ul className="divide-y rounded-lg border">
              {result.checks.map((c) => (
                <li key={c.name} className="flex items-start gap-3 px-3 py-2.5">
                  {c.ok ? (
                    <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-success" aria-label="passed" />
                  ) : (
                    <XCircle className="mt-0.5 size-4 shrink-0 text-destructive" aria-label="failed" />
                  )}
                  <div className="min-w-0 flex-1">
                    <div className="text-sm font-medium">{CHECK_LABEL[c.name] ?? c.name}</div>
                    <div className="font-mono text-[11px] text-muted-foreground">{c.name}</div>
                    {c.detail && (
                      <div className={cn("mt-1 text-xs break-words", c.ok ? "text-muted-foreground" : "text-destructive")}>
                        {c.detail}
                      </div>
                    )}
                  </div>
                  <span className="shrink-0 font-mono text-xs text-muted-foreground tabular-nums">
                    {formatMs(c.duration_ms)}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        ) : null}

        <DialogFooter>
          <Button variant="outline" onClick={onRetry} disabled={pending}>
            <Zap /> Run again
          </Button>
          <Button onClick={onClose}>Close</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ProfileCard({
  profile,
  onEdit,
  onDelete,
  onTest,
  testing,
}: {
  profile: StorageProfile;
  onEdit: () => void;
  onDelete: () => void;
  onTest: () => void;
  testing: boolean;
}) {
  return (
    <Card className="gap-4 py-5">
      <CardHeader className="flex flex-row items-start justify-between gap-3 px-5">
        <div className="flex min-w-0 items-start gap-3">
          <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-muted">
            <HardDrive className="size-4 text-muted-foreground" />
          </div>
          <div className="min-w-0">
            <CardTitle className="flex flex-wrap items-center gap-2 text-sm">
              <span className="truncate">{profile.name}</span>
              {profile.is_platform && (
                <StatusBadge tone="info" dot={false}>
                  <ShieldCheck className="size-3" /> platform
                </StatusBadge>
              )}
            </CardTitle>
            <p className="mt-0.5 text-xs text-muted-foreground">{PROVIDER_LABEL[profile.provider] ?? profile.provider}</p>
          </div>
        </div>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon-sm" aria-label={`Actions for ${profile.name}`}>
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onSelect={onEdit}>
              <Pencil /> Edit
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={onTest}>
              <Zap /> Test connection
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant="destructive" onSelect={onDelete}>
              <Trash2 /> Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </CardHeader>
      <CardContent className="grid gap-2 px-5 text-sm">
        <div className="flex items-center gap-2 min-w-0">
          <Globe className="size-3.5 shrink-0 text-muted-foreground" />
          <span className="font-mono text-xs">{profile.public_bucket}</span>
          <span className="truncate text-xs text-muted-foreground">→ {profile.public_base_url}</span>
        </div>
        <div className="flex items-center gap-2 min-w-0">
          <Lock className="size-3.5 shrink-0 text-muted-foreground" />
          <span className="font-mono text-xs">{profile.private_bucket}</span>
          {profile.private_base_url && (
            <span className="truncate text-xs text-muted-foreground">→ {profile.private_base_url}</span>
          )}
        </div>
        <div className="truncate font-mono text-xs text-muted-foreground" title={profile.endpoint}>
          {profile.endpoint} · {profile.region} · key {profile.access_key_id}
        </div>
      </CardContent>
      <CardFooter className="flex items-center justify-between gap-3 border-t px-5 pt-4">
        <div className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
          <TestStatusBadge ok={profile.last_test_ok} at={profile.last_test_at} />
          {profile.last_test_at && (
            <span className="hidden sm:inline">
              <RelativeTime value={profile.last_test_at} />
            </span>
          )}
        </div>
        <Button variant="outline" size="sm" onClick={onTest} disabled={testing}>
          {testing ? <Loader2 className="animate-spin" /> : <Zap />}
          Test connection
        </Button>
      </CardFooter>
    </Card>
  );
}

export default function StoragePage() {
  const search = route.useSearch();
  const navigate = route.useNavigate();
  const storage = useQuery(storageQuery);
  const del = useDeleteStorageProfile();
  const test = useTestStorageProfile();
  const [editing, setEditing] = useState<StorageProfile | null>(null);
  const [toDelete, setToDelete] = useState<StorageProfile | null>(null);
  const [testing, setTesting] = useState<StorageProfile | null>(null);

  const createOpen = !!search.new;
  const setCreateOpen = (o: boolean) => void navigate({ search: o ? { new: true } : {}, replace: true });

  function runTest(p: StorageProfile) {
    setTesting(p);
    test.mutate(p.id, {
      onSuccess: (r) =>
        r.ok ? toast.success(`${p.name}: all checks passed`) : toast.error(`${p.name}: some checks failed`),
    });
  }

  async function confirmDelete() {
    if (!toDelete) return;
    const p = toDelete;
    try {
      await del.mutateAsync(p.id);
      toast.success(`Storage profile “${p.name}” deleted`);
      setToDelete(null);
    } catch (e) {
      setToDelete(null);
      if (isApiError(e, "in_use")) {
        toast.error("Profile is in use", {
          description: "Move projects to another storage profile before deleting this one.",
        });
      } else {
        toast.error("Couldn't delete profile", { description: errorMessage(e) });
      }
    }
  }

  return (
    <>
      <PageHeader
        title="Storage"
        description="S3-compatible buckets where Kadr stores originals and renditions."
        actions={
          <Button onClick={() => setCreateOpen(true)}>
            <Plus /> Add storage
          </Button>
        }
      />

      {storage.isPending ? (
        <div className="grid gap-4 md:grid-cols-2">
          <Skeleton className="h-52 rounded-xl" />
          <Skeleton className="h-52 rounded-xl" />
        </div>
      ) : storage.isError ? (
        <ErrorState error={storage.error} onRetry={() => void storage.refetch()} />
      ) : storage.data.length === 0 ? (
        <EmptyState
          icon={<HardDrive />}
          title="No storage profiles"
          description="Connect Cloudflare R2 (or any S3-compatible storage) with a public and a private bucket."
          action={
            <Button onClick={() => setCreateOpen(true)}>
              <Plus /> Add R2 storage
            </Button>
          }
        />
      ) : (
        <div className="grid gap-4 md:grid-cols-2">
          {storage.data.map((p) => (
            <ProfileCard
              key={p.id}
              profile={p}
              onEdit={() => setEditing(p)}
              onDelete={() => setToDelete(p)}
              onTest={() => runTest(p)}
              testing={test.isPending && test.variables === p.id}
            />
          ))}
        </div>
      )}

      {createOpen && <StorageFormDialog open onOpenChange={setCreateOpen} />}
      {editing && <StorageFormDialog open profile={editing} onOpenChange={(o) => !o && setEditing(null)} />}

      <TestDialog
        profile={testing}
        result={test.data}
        pending={test.isPending}
        error={test.error}
        onRetry={() => testing && runTest(testing)}
        onClose={() => {
          setTesting(null);
          test.reset();
        }}
      />

      <AlertDialog open={!!toDelete} onOpenChange={(o) => !o && setToDelete(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete “{toDelete?.name}”?</AlertDialogTitle>
            <AlertDialogDescription>
              Kadr forgets this profile and its encrypted credentials. Files in the buckets are not touched. Profiles
              used by a project can't be deleted.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={(e) => {
                e.preventDefault();
                void confirmDelete();
              }}
              disabled={del.isPending}
            >
              {del.isPending && <Loader2 className="animate-spin" />}
              Delete profile
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
