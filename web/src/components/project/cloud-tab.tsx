import { useQuery } from "@tanstack/react-query";
import { Cloud, Loader2, Plus, Trash2 } from "lucide-react";
import { useState } from "react";

import { Field, RelativeTime } from "@/components/common";
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
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { errorMessage, isApiError } from "@/lib/api";
import { cloudQuery, useCreateCloudConnection, useRevokeCloudConnection } from "@/lib/queries";
import { CLOUD_PROVIDERS, type CloudConnection, type CloudProvider, type Project } from "@/lib/types";
import { toast } from "sonner";

const PROVIDER_LABEL: Record<CloudProvider, string> = { gdrive: "Google Drive", onedrive: "OneDrive", dropbox: "Dropbox" };
const RCLONE_NAME: Record<CloudProvider, string> = { gdrive: "drive", onedrive: "onedrive", dropbox: "dropbox" };

function CreateDialog({ project, open, onOpenChange }: { project: Project; open: boolean; onOpenChange: (o: boolean) => void }) {
  const create = useCreateCloudConnection(project.id);
  const [provider, setProvider] = useState<CloudProvider>("gdrive");
  const [label, setLabel] = useState("");
  const [namespace, setNamespace] = useState("");
  const [token, setToken] = useState("");
  const fe = isApiError(create.error) ? create.error.fieldErrors : {};
  const other = create.isError && Object.keys(fe).length === 0 ? errorMessage(create.error) : undefined;

  const close = (o: boolean) => {
    if (!o) {
      setLabel("");
      setNamespace("");
      setToken("");
      create.reset();
    }
    onOpenChange(o);
  };

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="max-h-[92svh] overflow-y-auto sm:max-w-lg">
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate(
              {
                provider,
                token: token.trim(),
                account_label: label.trim() || undefined,
                namespace: namespace.trim() || undefined,
              },
              {
                onSuccess: () => {
                  toast.success("Cloud connection added");
                  close(false);
                },
              },
            );
          }}
        >
          <DialogHeader>
            <DialogTitle>Add cloud connection</DialogTitle>
            <DialogDescription>
              Lets API clients import files from a cloud drive. The token is stored encrypted and never shown again.
            </DialogDescription>
          </DialogHeader>
          <Field label="Provider" htmlFor="cc-provider" error={fe.provider}>
            <Select value={provider} onValueChange={(v) => setProvider(v as CloudProvider)}>
              <SelectTrigger id="cc-provider" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CLOUD_PROVIDERS.map((p) => (
                  <SelectItem key={p} value={p}>
                    {PROVIDER_LABEL[p]}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Account label" htmlFor="cc-label" hint="Optional, e.g. an email address.">
              <Input id="cc-label" value={label} onChange={(e) => setLabel(e.target.value)} />
            </Field>
            <Field label="Namespace" htmlFor="cc-ns" hint="Optional restriction." error={fe.namespace}>
              <Input id="cc-ns" value={namespace} onChange={(e) => setNamespace(e.target.value)} className="font-mono" />
            </Field>
          </div>
          <Field
            label="rclone token (JSON)"
            htmlFor="cc-token"
            error={fe.token}
            hint={
              <>
                Run <code className="font-mono">rclone authorize "{RCLONE_NAME[provider]}"</code> and paste the resulting JSON.
              </>
            }
          >
            <Textarea
              id="cc-token"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              rows={5}
              spellCheck={false}
              autoComplete="off"
              placeholder='{"access_token":"…","token_type":"Bearer","refresh_token":"…","expiry":"…"}'
              className="font-mono text-xs"
              aria-invalid={!!fe.token}
            />
          </Field>
          {other && (
            <p className="text-sm text-destructive" role="alert">
              {other}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => close(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={!token.trim() || create.isPending}>
              {create.isPending && <Loader2 className="animate-spin" />}
              Add connection
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function CloudTab({ project }: { project: Project }) {
  const conns = useQuery(cloudQuery(project.id));
  const revoke = useRevokeCloudConnection(project.id);
  const [createOpen, setCreateOpen] = useState(false);
  const [toRevoke, setToRevoke] = useState<CloudConnection | null>(null);
  const list = conns.data ?? [];

  return (
    <div className="grid gap-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="text-sm text-muted-foreground">Connected drives can be used as import sources through the Kadr API.</p>
        <Button onClick={() => setCreateOpen(true)} disabled={project.disabled} className="shrink-0">
          <Plus /> Add connection
        </Button>
      </div>

      {conns.isPending ? (
        <Skeleton className="h-24 w-full" />
      ) : conns.isError ? (
        <ErrorState error={conns.error} onRetry={() => void conns.refetch()} />
      ) : list.length === 0 ? (
        <EmptyState
          icon={<Cloud />}
          title="No cloud connections"
          description="Connect Google Drive, OneDrive or Dropbox to import media."
          action={
            <Button onClick={() => setCreateOpen(true)} disabled={project.disabled}>
              <Plus /> Add connection
            </Button>
          }
        />
      ) : (
        <div className="overflow-hidden rounded-xl border bg-card">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="pl-4">Provider</TableHead>
                <TableHead className="hidden sm:table-cell">Namespace</TableHead>
                <TableHead className="hidden sm:table-cell">Added</TableHead>
                <TableHead className="w-12 pr-4" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.map((c) => (
                <TableRow key={c.id}>
                  <TableCell className="pl-4">
                    <div className="font-medium">{PROVIDER_LABEL[c.provider as CloudProvider] ?? c.provider}</div>
                    <div className="text-xs text-muted-foreground">{c.account_label || <span className="font-mono">{c.id}</span>}</div>
                  </TableCell>
                  <TableCell className="hidden font-mono text-xs sm:table-cell">{c.namespace ?? "all"}</TableCell>
                  <TableCell className="hidden text-sm text-muted-foreground sm:table-cell">
                    <RelativeTime value={c.created_at} />
                  </TableCell>
                  <TableCell className="pr-3">
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      aria-label="Revoke connection"
                      className="text-muted-foreground hover:text-destructive"
                      onClick={() => setToRevoke(c)}
                    >
                      <Trash2 />
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      <CreateDialog project={project} open={createOpen} onOpenChange={setCreateOpen} />

      <AlertDialog open={!!toRevoke} onOpenChange={(o) => !o && setToRevoke(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Revoke this connection?</AlertDialogTitle>
            <AlertDialogDescription>
              The stored token is deleted and imports from this account will fail. This can't be undone.
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
              Revoke connection
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
