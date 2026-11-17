import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Loader2, Plus, RotateCcw, Send, Trash2, Webhook } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import { Field, RelativeTime, StatusBadge } from "@/components/common";
import { SecretDialog } from "@/components/project/secret-dialog";
import { FilterSelect } from "@/components/project-select";
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
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, errorMessage, isApiError } from "@/lib/api";
import {
  qk,
  useCreateWebhook,
  useDeleteWebhook,
  useRetryDelivery,
  useTestWebhook,
  webhooksQuery,
} from "@/lib/queries";
import { DELIVERY_STATUSES, WEBHOOK_EVENTS, type CreateWebhookResponse, type Delivery, type Project, type WebhookEndpoint } from "@/lib/types";
import { cn } from "@/lib/utils";

type Tone = "success" | "danger" | "warning" | "neutral" | "info";
const DELIVERY_TONE: Record<string, Tone> = { delivered: "success", dead: "danger", pending: "warning", delivering: "info" };

function CreateWebhookDialog({
  project,
  open,
  onOpenChange,
  onCreated,
}: {
  project: Project;
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onCreated: (r: CreateWebhookResponse) => void;
}) {
  const create = useCreateWebhook(project.id);
  const [url, setUrl] = useState("");
  const [events, setEvents] = useState<string[]>([]);
  const urlErr = isApiError(create.error) ? create.error.fieldErrors.url : undefined;
  const evErr = isApiError(create.error) ? create.error.fieldErrors.events : undefined;
  const otherErr = create.isError && !urlErr && !evErr ? errorMessage(create.error) : undefined;

  const reset = () => {
    setUrl("");
    setEvents([]);
    create.reset();
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o) reset();
        onOpenChange(o);
      }}
    >
      <DialogContent className="max-h-[92svh] overflow-y-auto sm:max-w-lg">
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate(
              { url: url.trim(), events },
              {
                onSuccess: (r) => {
                  reset();
                  onOpenChange(false);
                  onCreated(r);
                },
              },
            );
          }}
        >
          <DialogHeader>
            <DialogTitle>Add webhook endpoint</DialogTitle>
            <DialogDescription>Kadr POSTs signed JSON events to this URL.</DialogDescription>
          </DialogHeader>
          <Field label="Endpoint URL" htmlFor="wh-url" error={urlErr}>
            <Input
              id="wh-url"
              type="url"
              required
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder="https://example.com/hooks/kadr"
              aria-invalid={!!urlErr}
            />
          </Field>
          <fieldset className="grid gap-2">
            <legend className="mb-1 text-sm font-medium">Events</legend>
            <p className="text-xs text-muted-foreground">Leave all unchecked to receive every event.</p>
            <div className="grid max-h-48 grid-cols-2 gap-x-4 gap-y-2 overflow-y-auto rounded-lg border p-3">
              {WEBHOOK_EVENTS.map((ev) => (
                <label key={ev} className="flex items-center gap-2 font-mono text-xs">
                  <Checkbox
                    checked={events.includes(ev)}
                    onCheckedChange={(c) => setEvents((cur) => (c === true ? [...cur, ev] : cur.filter((x) => x !== ev)))}
                  />
                  {ev}
                </label>
              ))}
            </div>
            {evErr && (
              <p className="text-xs text-destructive" role="alert">
                {evErr}
              </p>
            )}
          </fieldset>
          {otherErr && (
            <p className="text-sm text-destructive" role="alert">
              {otherErr}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={!url.trim() || create.isPending}>
              {create.isPending && <Loader2 className="animate-spin" />}
              Add endpoint
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function Endpoints({ project }: { project: Project }) {
  const endpoints = useQuery(webhooksQuery(project.id));
  const del = useDeleteWebhook(project.id);
  const test = useTestWebhook(project.id);
  const [createOpen, setCreateOpen] = useState(false);
  const [created, setCreated] = useState<CreateWebhookResponse | null>(null);
  const [toDelete, setToDelete] = useState<WebhookEndpoint | null>(null);
  const [testing, setTesting] = useState<string | null>(null);
  const list = endpoints.data ?? [];

  const runTest = (e: WebhookEndpoint) => {
    setTesting(e.id);
    test.mutate(e.id, {
      onSuccess: (d) => {
        if (d.status === "delivered") toast.success("Test event delivered", { description: `HTTP ${d.last_status_code ?? "—"} in ${d.last_duration_ms ?? "?"} ms` });
        else
          toast.error("Test delivery failed", {
            description: d.last_error ?? (d.last_status_code ? `HTTP ${d.last_status_code}` : `Status: ${d.status}`),
          });
      },
      onError: (err) => toast.error("Couldn't send test event", { description: errorMessage(err) }),
      onSettled: () => setTesting(null),
    });
  };

  return (
    <section className="grid gap-3">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="text-sm text-muted-foreground">Endpoints receive signed event notifications (asset.ready, asset.failed, …).</p>
        <Button onClick={() => setCreateOpen(true)} disabled={project.disabled} className="shrink-0">
          <Plus /> Add endpoint
        </Button>
      </div>

      {endpoints.isPending ? (
        <div className="space-y-2 rounded-xl border bg-card p-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : endpoints.isError ? (
        <ErrorState error={endpoints.error} onRetry={() => void endpoints.refetch()} />
      ) : list.length === 0 ? (
        <EmptyState
          icon={<Webhook />}
          title="No webhook endpoints"
          description="Add an endpoint to be notified when assets finish processing."
          action={
            <Button onClick={() => setCreateOpen(true)} disabled={project.disabled}>
              <Plus /> Add endpoint
            </Button>
          }
        />
      ) : (
        <div className="overflow-hidden rounded-xl border bg-card">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="pl-4">URL</TableHead>
                <TableHead className="hidden md:table-cell">Events</TableHead>
                <TableHead className="hidden sm:table-cell">Created</TableHead>
                <TableHead className="w-28 pr-4" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.map((e) => (
                <TableRow key={e.id} className={cn(e.disabled && "opacity-60")}>
                  <TableCell className="max-w-[20rem] pl-4">
                    <div className="truncate font-mono text-xs font-medium" title={e.url}>
                      {e.url}
                    </div>
                    <div className="flex items-center gap-2 font-mono text-[11px] text-muted-foreground">
                      {e.id}
                      {e.disabled && <StatusBadge tone="neutral">disabled</StatusBadge>}
                    </div>
                  </TableCell>
                  <TableCell className="hidden max-w-[16rem] md:table-cell">
                    <div className="flex flex-wrap gap-1">
                      {e.events.map((ev) => (
                        <span key={ev} className="rounded bg-secondary px-1.5 py-0.5 font-mono text-[11px] whitespace-nowrap">
                          {ev === "*" ? "all events" : ev}
                        </span>
                      ))}
                    </div>
                  </TableCell>
                  <TableCell className="hidden text-sm text-muted-foreground sm:table-cell">
                    <RelativeTime value={e.created_at} />
                  </TableCell>
                  <TableCell className="pr-3 text-right whitespace-nowrap">
                    <Button variant="ghost" size="sm" disabled={testing === e.id} onClick={() => runTest(e)}>
                      {testing === e.id ? <Loader2 className="animate-spin" /> : <Send />} Test
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      aria-label={`Delete ${e.url}`}
                      className="text-muted-foreground hover:text-destructive"
                      onClick={() => setToDelete(e)}
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

      <CreateWebhookDialog project={project} open={createOpen} onOpenChange={setCreateOpen} onCreated={setCreated} />
      <SecretDialog
        open={!!created}
        onDone={() => setCreated(null)}
        title="Save the signing secret"
        description={
          <>
            Endpoint <span className="font-mono text-xs text-foreground">{created?.webhook.url}</span> was created. Use this secret to
            verify the <code className="font-mono text-xs">webhook-signature</code> header.
          </>
        }
        secret={created?.secret ?? ""}
      />

      <AlertDialog open={!!toDelete} onOpenChange={(o) => !o && setToDelete(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete this endpoint?</AlertDialogTitle>
            <AlertDialogDescription>
              <span className="font-mono text-xs break-all">{toDelete?.url}</span> will stop receiving events and its delivery history is
              removed. This can't be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => {
                if (toDelete) del.mutate(toDelete.id);
                setToDelete(null);
              }}
            >
              Delete endpoint
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}

function DeliveryRow({ d, onRetry, retrying }: { d: Delivery; onRetry: () => void; retrying: boolean }) {
  return (
    <TableRow>
      <TableCell className="pl-4">
        <div className="font-mono text-xs font-medium">{d.event_type}</div>
        <div className="max-w-[16rem] truncate font-mono text-[11px] text-muted-foreground" title={d.endpoint_url}>
          {d.endpoint_url}
        </div>
      </TableCell>
      <TableCell>
        <StatusBadge tone={DELIVERY_TONE[d.status] ?? "neutral"}>{d.status}</StatusBadge>
      </TableCell>
      <TableCell className="hidden text-sm tabular-nums sm:table-cell">{d.attempts}</TableCell>
      <TableCell className="hidden max-w-[14rem] md:table-cell">
        {d.last_error ? (
          <span className="block truncate text-xs text-destructive" title={d.last_error}>
            {d.last_error}
          </span>
        ) : d.last_status_code ? (
          <span className="text-xs text-muted-foreground">
            HTTP {d.last_status_code}
            {d.last_duration_ms !== null && ` · ${d.last_duration_ms} ms`}
          </span>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </TableCell>
      <TableCell className="hidden text-sm text-muted-foreground lg:table-cell">
        <RelativeTime value={d.created_at} />
      </TableCell>
      <TableCell className="pr-3 text-right">
        {d.status !== "delivering" && (
          <Button variant="ghost" size="sm" disabled={retrying} onClick={onRetry}>
            <RotateCcw /> Retry
          </Button>
        )}
      </TableCell>
    </TableRow>
  );
}

function Deliveries({ project }: { project: Project }) {
  const [status, setStatus] = useState<string | undefined>();
  const retry = useRetryDelivery(project.id);
  const q = useInfiniteQuery({
    queryKey: [...qk.deliveries(project.id), status ?? "all"],
    queryFn: ({ pageParam }) => api.webhooks.deliveries(project.id, { status, cursor: pageParam, limit: 25 }),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.next_cursor || null,
  });
  const items = q.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <section className="grid gap-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h3 className="text-sm font-semibold">Deliveries</h3>
        <div className="flex items-center gap-2">
          <FilterSelect value={status} onChange={setStatus} options={DELIVERY_STATUSES} allLabel="All statuses" label="Delivery status" />
          <Button variant="outline" size="sm" onClick={() => void q.refetch()} disabled={q.isFetching}>
            {q.isFetching && !q.isFetchingNextPage ? <Loader2 className="animate-spin" /> : <RotateCcw />} Refresh
          </Button>
        </div>
      </div>
      {q.isPending ? (
        <Skeleton className="h-32 w-full" />
      ) : q.isError ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : items.length === 0 ? (
        <EmptyState title="No deliveries" description={status ? "No deliveries with this status." : "Events sent to your endpoints will be logged here."} />
      ) : (
        <>
          <div className="overflow-hidden rounded-xl border bg-card">
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="pl-4">Event</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="hidden sm:table-cell">Attempts</TableHead>
                  <TableHead className="hidden md:table-cell">Last result</TableHead>
                  <TableHead className="hidden lg:table-cell">Created</TableHead>
                  <TableHead className="w-24 pr-4" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((d) => (
                  <DeliveryRow
                    key={d.id}
                    d={d}
                    retrying={retry.isPending && retry.variables === d.id}
                    onRetry={() => retry.mutate(d.id)}
                  />
                ))}
              </TableBody>
            </Table>
          </div>
          {q.hasNextPage && (
            <div className="flex justify-end">
              <Button variant="outline" size="sm" onClick={() => void q.fetchNextPage()} disabled={q.isFetchingNextPage}>
                {q.isFetchingNextPage && <Loader2 className="animate-spin" />}
                Load more
              </Button>
            </div>
          )}
        </>
      )}
    </section>
  );
}

export function WebhooksTab({ project }: { project: Project }) {
  return (
    <div className="grid gap-8">
      <Endpoints project={project} />
      <Deliveries project={project} />
    </div>
  );
}
