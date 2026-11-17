import { useQuery } from "@tanstack/react-query";
import { CloudUpload, KeyRound, Loader2, RefreshCw, Trash2 } from "lucide-react";
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
import { useT } from "@/i18n/react";
import { errorMessage, isApiError } from "@/lib/api";
import { signingKeysQuery, usePublishSigningKeys, useRetireSigningKey, useRotateSigningKey } from "@/lib/queries";
import type { CreateSigningKeyResponse, Project, SigningKey } from "@/lib/types";
import { cn } from "@/lib/utils";
import { toast } from "sonner";

const TONE = { active: "success", verify_only: "warning", retired: "neutral" } as const;

export function SigningKeysTab({ project }: { project: Project }) {
  const t = useT();
  const keys = useQuery(signingKeysQuery(project.id));
  const rotate = useRotateSigningKey(project.id);
  const retire = useRetireSigningKey(project.id);
  const publish = usePublishSigningKeys(project.id);
  const [publishError, setPublishError] = useState<{ notConfigured: boolean; message: string } | null>(null);
  const [confirmRotate, setConfirmRotate] = useState(false);
  const [created, setCreated] = useState<CreateSigningKeyResponse | null>(null);
  const [toRetire, setToRetire] = useState<SigningKey | null>(null);
  const list = keys.data ?? [];
  const hasActive = list.some((k) => k.status === "active");

  const doRotate = () => {
    setConfirmRotate(false);
    rotate.mutate(undefined, {
      onSuccess: setCreated,
      onError: (e) => toast.error(t("signing.rotateFailed"), { description: errorMessage(e) }),
    });
  };

  const doPublish = () => {
    setPublishError(null);
    publish.mutate(undefined, {
      onSuccess: (r) => toast.success(t("signing.published", { count: r.published })),
      onError: (e) => {
        const notConfigured = isApiError(e) && e.status === 503 && e.code === "edge_not_configured";
        setPublishError({ notConfigured, message: errorMessage(e) });
        if (!notConfigured) toast.error(t("signing.publishFailed"), { description: errorMessage(e) });
      },
    });
  };

  return (
    <div className="grid gap-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="text-sm text-muted-foreground">
          {t("signing.intro")}
        </p>
        <div className="flex shrink-0 gap-2">
        <Button variant="outline" onClick={doPublish} disabled={publish.isPending || list.length === 0}>
          {publish.isPending ? <Loader2 className="animate-spin" /> : <CloudUpload />}
          {t("signing.publish")}
        </Button>
        <Button
          onClick={() => (hasActive ? setConfirmRotate(true) : doRotate())}
          disabled={project.disabled || rotate.isPending}
          className="shrink-0"
        >
          {rotate.isPending ? <Loader2 className="animate-spin" /> : <RefreshCw />}
          {hasActive ? t("signing.rotate") : t("signing.create")}
        </Button>
        </div>
      </div>

      {publishError && (
        <p className="rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-sm" role="alert">
          {publishError.notConfigured ? (
            <>
              {t("signing.edgeNotConfiguredPre")} <code className="font-mono text-xs">KADR_CF_ACCOUNT_ID</code> {t("signing.and")}{" "}
              <code className="font-mono text-xs">KADR_CF_API_TOKEN</code> {t("signing.edgeNotConfiguredPost")}
            </>
          ) : (
            <>{t("signing.publishErrorPrefix")} {publishError.message}</>
          )}
        </p>
      )}

      {keys.isPending ? (
        <Skeleton className="h-24 w-full" />
      ) : keys.isError ? (
        <ErrorState error={keys.error} onRetry={() => void keys.refetch()} />
      ) : list.length === 0 ? (
        <EmptyState icon={<KeyRound />} title={t("signing.emptyTitle")} description={t("signing.emptyDesc")} />
      ) : (
        <div className="overflow-hidden rounded-xl border bg-card">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="pl-4">{t("signing.colKeyId")}</TableHead>
                <TableHead>{t("signing.colStatus")}</TableHead>
                <TableHead className="hidden sm:table-cell">{t("signing.colCreated")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("signing.colPublished")}</TableHead>
                <TableHead className="w-12 pr-4" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.map((k) => (
                <TableRow key={k.id} className={cn(k.status === "retired" && "opacity-60")}>
                  <TableCell className="pl-4 font-mono text-xs font-medium">{k.id}</TableCell>
                  <TableCell>
                    <StatusBadge tone={(TONE as Record<string, "success" | "warning" | "neutral">)[k.status] ?? "neutral"}>
                      {k.status === "active" || k.status === "verify_only" || k.status === "retired" ? t(`signing.status.${k.status}` as const) : k.status}
                    </StatusBadge>
                  </TableCell>
                  <TableCell className="hidden text-sm text-muted-foreground sm:table-cell">
                    <RelativeTime value={k.created_at} />
                  </TableCell>
                  <TableCell className="hidden text-sm text-muted-foreground md:table-cell">
                    <RelativeTime value={k.published_at} fallback={t("signing.notPublished")} />
                  </TableCell>
                  <TableCell className="pr-3">
                    {k.status !== "retired" && (
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={t("signing.retireAria", { id: k.id })}
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
        title={t("signing.saveTitle")}
        description={
          <>
            {t("signing.keyPre")} <span className="font-mono text-xs text-foreground">{created?.key.id}</span> {t("signing.keyPost")}
          </>
        }
        secret={created?.secret ?? ""}
      />

      <AlertDialog open={confirmRotate} onOpenChange={setConfirmRotate}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("signing.rotateTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("signing.rotateDescPre")} <code className="font-mono text-xs">SIGNING_KEYS</code>{t("signing.rotateDescPost")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("signing.cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={doRotate}>{t("signing.rotate")}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={!!toRetire} onOpenChange={(o) => !o && setToRetire(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("signing.retireTitle", { id: toRetire?.id ?? "" })}</AlertDialogTitle>
            <AlertDialogDescription>{t("signing.retireDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("signing.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => {
                if (toRetire) retire.mutate(toRetire.id);
                setToRetire(null);
              }}
            >
              {t("signing.retire")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
