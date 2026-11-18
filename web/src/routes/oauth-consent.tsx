import { useQuery } from "@tanstack/react-query";
import { getRouteApi } from "@tanstack/react-router";
import { AlertTriangle, Bot, Loader2, ShieldX } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import { AgentFields } from "@/components/agents/agent-form";
import { agentStateToInput, initialAgentState, validateAgentState, type AgentState } from "@/components/agents/agent-state";
import { LEVEL_LABEL } from "@/components/agents/labels";
import { Field, PageHeader, RelativeTime } from "@/components/common";
import { EmptyState, ErrorState } from "@/components/states";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { useT } from "@/i18n/react";
import { api, errorMessage, isApiError } from "@/lib/api";
import { formatDate } from "@/lib/format";
import { oauthGrantQuery } from "@/lib/queries";
import type { OAuthGrant } from "@/lib/types";
import { useRole } from "@/lib/use-role";

const route = getRouteApi("/app/oauth/consent");

export default function OAuthConsentPage() {
  const t = useT();
  const { grant: grantId } = route.useSearch();
  const grant = useQuery({ ...oauthGrantQuery(grantId ?? ""), enabled: !!grantId });

  if (!grantId) {
    return <EmptyState icon={<ShieldX />} title={t("agents.oauth.expiredTitle")} description={t("agents.oauth.missingGrant")} />;
  }
  if (grant.isPending) {
    return (
      <div className="mx-auto max-w-2xl space-y-3">
        <Skeleton className="h-10 w-2/3" />
        <Skeleton className="h-32 w-full" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }
  if (grant.isError) {
    if (isApiError(grant.error, "not_pending") || isApiError(grant.error, "not_found")) {
      return <EmptyState icon={<ShieldX />} title={t("agents.oauth.expiredTitle")} description={t("agents.oauth.expiredDesc")} />;
    }
    return <ErrorState error={grant.error} onRetry={() => void grant.refetch()} />;
  }
  return <Consent grant={grant.data} />;
}

function Consent({ grant }: { grant: OAuthGrant }) {
  const t = useT();
  const { canManage } = useRole();
  const [s, setS] = useState<AgentState>(() => ({ ...initialAgentState(undefined, grant.suggested_level), name: grant.client_name }));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState<"approve" | "deny" | "redirecting" | null>(null);

  async function approve(ev: React.FormEvent) {
    ev.preventDefault();
    const e = validateAgentState(s, t);
    setErrors(e);
    if (Object.keys(e).length) return;
    setBusy("approve");
    try {
      const res = await api.oauth.approve(grant.id, agentStateToInput(s));
      setBusy("redirecting");
      window.location.assign(res.redirect_url);
    } catch (err) {
      setBusy(null);
      if (isApiError(err, "validation_failed") && Object.keys(err.fieldErrors).length) {
        setErrors(err.fieldErrors);
      } else {
        toast.error(t("agents.oauth.approveFailed"), { description: errorMessage(err) });
      }
    }
  }

  async function deny() {
    setBusy("deny");
    try {
      const res = await api.oauth.deny(grant.id);
      setBusy("redirecting");
      window.location.assign(res.redirect_url);
    } catch (err) {
      setBusy(null);
      toast.error(t("agents.oauth.denyFailed"), { description: errorMessage(err) });
    }
  }

  return (
    <div className="mx-auto max-w-2xl">
      <PageHeader title={t("agents.oauth.title")} description={t("agents.oauth.description")} />

      <div className="mb-6 rounded-xl border border-warning/40 bg-warning/5 p-4">
        <div className="flex items-start gap-3">
          <AlertTriangle className="mt-0.5 size-5 shrink-0 text-warning" />
          <div className="grid gap-1 text-sm">
            <p className="font-medium">{t("agents.oauth.warning", { client: grant.client_name, host: grant.redirect_host })}</p>
            <p className="text-muted-foreground">{t("agents.oauth.unknownHost", { host: grant.redirect_host })}</p>
          </div>
        </div>
        <dl className="mt-4 grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
          <div>
            <dt className="text-xs text-muted-foreground">{t("agents.oauth.client")}</dt>
            <dd className="flex items-center gap-1.5 font-medium">
              <Bot className="size-4 text-brand" />
              {grant.client_name}
            </dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">{t("agents.oauth.clientId")}</dt>
            <dd className="truncate font-mono text-xs">{grant.client_id}</dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">{t("agents.oauth.registered")}</dt>
            <dd>
              <RelativeTime value={grant.client_registered_at} />
            </dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">{t("agents.oauth.requested")}</dt>
            <dd>{t(LEVEL_LABEL[grant.suggested_level])}</dd>
          </div>
          <div className="sm:col-span-2">
            <dt className="text-xs text-muted-foreground">{t("agents.oauth.expires")}</dt>
            <dd>{formatDate(grant.expires_at)}</dd>
          </div>
        </dl>
      </div>

      {!canManage ? (
        <p className="rounded-lg border bg-muted/40 px-3 py-2 text-sm text-muted-foreground">{t("agents.oauth.viewerCannot")}</p>
      ) : busy === "redirecting" ? (
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="size-4 animate-spin" />
          {t("agents.oauth.redirecting", { host: grant.redirect_host })}
        </p>
      ) : (
        <form onSubmit={approve} noValidate className="grid gap-5">
          <Field label={t("agents.oauth.name")} htmlFor="oauth-name" error={errors.name}>
            <Input id="oauth-name" value={s.name} aria-invalid={!!errors.name} onChange={(e) => setS({ ...s, name: e.target.value })} />
          </Field>
          <AgentFields state={s} onChange={setS} errors={errors} hideName />
          <div className="flex flex-wrap justify-end gap-2 border-t pt-4">
            <Button type="button" variant="outline" disabled={!!busy} onClick={() => void deny()}>
              {busy === "deny" && <Loader2 className="animate-spin" />}
              {t("agents.oauth.deny")}
            </Button>
            <Button type="submit" disabled={!!busy}>
              {busy === "approve" && <Loader2 className="animate-spin" />}
              {t("agents.oauth.approve")}
            </Button>
          </div>
        </form>
      )}
    </div>
  );
}
