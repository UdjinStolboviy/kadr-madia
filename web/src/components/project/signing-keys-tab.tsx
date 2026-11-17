import { useQuery } from "@tanstack/react-query";
import { KeyRound, Loader2, RefreshCw, Trash2 } from "lucide-react";
import { useState } from "react";

import { RelativeTime, StatusBadge } from "@/components/common";
import { SecretDialog } from "@/components/project/secret-dialog";
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
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { errorMessage } from "@/lib/api";
import { signingKeysQuery, useRetireSigningKey, useRotateSigningKey } from "@/lib/queries";
import type { CreateSigningKeyResponse, Project, SigningKey } from "@/lib/types";
import { cn } from "@/lib/utils";
import { toast } from "sonner";

const TONE = { active: "success", verify_only: "warning", retired: "neutral" } as const;

export function SigningKeysTab({ project }: { project: Project }) {
  const keys = useQuery(signingKeysQuery(project.id));
  const rotate = useRotateSigningKey(project.id);
  const retire = useRetireSigningKey(project.id);
  const [confirmRotate, setConfirmRotate] = useState(false);
  const [created, setCreated] = useState<CreateSigningKeyResponse | null>(null);
  const [toRetire, setToRetire] = useState<SigningKey | null>(null);
  const list = keys.data ?? [];
  const hasActive = list.some((k) => k.status === "active");

  const doRotate = () => {
    setConfirmRotate(false);
    rotate.mutate(undefined, {
      onSuccess: setCreated,
      onError: (e) => toast.error("Couldn't rotate signing key", { description: errorMessage(e) }),
    });
  };

  return (
    <div className="grid gap-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="text-sm text-muted-foreground">
          Signing keys sign private delivery URLs. Rotating creates a new active key; the previous one keeps verifying existing URLs
          until you retire it.
        </p>
        <Button
          onClick={() => (hasActive ? setConfirmRotate(true) : doRotate())}
          disabled={project.disabled || rotate.isPending}
          className="shrink-0"
        >
          {rotate.isPending ? <Loader2 className="animate-spin" /> : <RefreshCw />}
          {hasActive ? "Rotate key" : "Create key"}
        </Button>
      </div>

      {keys.isPending ? (
        <Skeleton className="h-24 w-full" />
      ) : keys.isError ? (
        <ErrorState error={keys.error} onRetry={() => void keys.refetch()} />
      ) : list.length === 0 ? (
        <EmptyState icon={<KeyRound />} title="No signing keys" description="Create a key to sign private asset URLs." />
      ) : (
        <div className="overflow-hidden rounded-xl border bg-card">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="pl-4">Key ID</TableHead>
                <TableHead>Status</TableHead>
                <TableHead className="hidden sm:table-cell">Created</TableHead>
                <TableHead className="w-12 pr-4" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.map((k) => (
                <TableRow key={k.id} className={cn(k.status === "retired" && "opacity-60")}>
                  <TableCell className="pl-4 font-mono text-xs font-medium">{k.id}</TableCell>
                  <TableCell>
                    <StatusBadge tone={(TONE as Record<string, "success" | "warning" | "neutral">)[k.status] ?? "neutral"}>
                      {k.status.replace("_", " ")}
                    </StatusBadge>
                  </TableCell>
                  <TableCell className="hidden text-sm text-muted-foreground sm:table-cell">
                    <RelativeTime value={k.created_at} />
                  </TableCell>
                  <TableCell className="pr-3">
                    {k.status !== "retired" && (
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={`Retire ${k.id}`}
                        className="text-muted-foreground hover:text-destructive"
                        onClick={() => setToRetire(k)}
                      >
                        <Trash2 />
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      <SecretDialog
        open={!!created}
        onDone={() => setCreated(null)}
        title="Save the signing key secret"
        description={
          <>
            Key <span className="font-mono text-xs text-foreground">{created?.key.id}</span> is now active.
          </>
        }
        secret={created?.secret ?? ""}
      />

      <AlertDialog open={confirmRotate} onOpenChange={setConfirmRotate}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Rotate the signing key?</AlertDialogTitle>
            <AlertDialogDescription>
              A new key becomes active for signing. The current key moves to verify-only so URLs already issued keep working. Update the
              kadr-edge <code className="font-mono text-xs">SIGNING_KEYS</code> secret afterwards.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={doRotate}>Rotate key</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={!!toRetire} onOpenChange={(o) => !o && setToRetire(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Retire “{toRetire?.id}”?</AlertDialogTitle>
            <AlertDialogDescription>URLs signed with this key stop verifying at the edge. This can't be undone.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => {
                if (toRetire) retire.mutate(toRetire.id);
                setToRetire(null);
              }}
            >
              Retire key
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
