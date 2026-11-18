import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Loader2 } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import { Field } from "@/components/common";
import { TagInput } from "@/components/tag-input";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import type { MessageKey } from "@/i18n/dictionaries";
import { useT } from "@/i18n/react";
import { errorMessage, isApiError } from "@/lib/api";
import { formatDate } from "@/lib/format";
import { agentSettingsQuery, projectsQuery, useCreateAgent, useUpdateAgent } from "@/lib/queries";
import {
  AGENT_APPROVAL_MODES,
  AGENT_LEVELS,
  AGENT_SENSITIVE_SCOPES,
  type Agent,
  type AgentApprovalMode,
  type AgentInput,
  type AgentLevel,
  type AgentSecretResponse,
} from "@/lib/types";
import { useRole } from "@/lib/use-role";

import { APPROVAL_DESC, APPROVAL_LABEL, LEVEL_DESC, LEVEL_LABEL } from "./labels";
import { cn } from "@/lib/utils";

const EXPIRY_DAYS = [30, 90, 180, 365] as const;

const IP_RE = /^(\d{1,3}(\.\d{1,3}){3}|[0-9a-fA-F:]+)(\/\d{1,3})?$/;

interface State {
  name: string;
  description: string;
  level: AgentLevel;
  extra: string[];
  allProjects: boolean;
  projects: string[];
  approval: AgentApprovalMode;
  ips: string[];
  deletes: string;
  expiry: string; // "keep" | "0" | days
}

function initial(agent?: Agent): State {
  return {
    name: agent?.name ?? "",
    description: agent?.description ?? "",
    level: agent?.level ?? "read",
    extra: agent?.extra_scopes ?? [],
    allProjects: agent ? agent.project_ids === null : true,
    projects: agent?.project_ids ?? [],
    approval: agent?.approval_mode ?? "destructive",
    ips: agent?.allowed_ips ?? [],
    deletes: String(agent?.max_deletes_per_hour ?? 50),
    expiry: agent ? "keep" : "90",
  };
}

/** Create or edit an agent token. On create, `onCreated` receives the one-time secret. */
export function AgentFormSheet({
  open,
  agent,
  onOpenChange,
  onCreated,
}: {
  open: boolean;
  agent?: Agent;
  onOpenChange: (open: boolean) => void;
  onCreated: (res: AgentSecretResponse) => void;
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 sm:max-w-xl">
        {open && <AgentForm key={agent?.id ?? "new"} agent={agent} onClose={() => onOpenChange(false)} onCreated={onCreated} />}
      </SheetContent>
    </Sheet>
  );
}

function AgentForm({ agent, onClose, onCreated }: { agent?: Agent; onClose: () => void; onCreated: (r: AgentSecretResponse) => void }) {
  const t = useT();
  const { isOwner } = useRole();
  const projects = useQuery(projectsQuery);
  const settings = useQuery(agentSettingsQuery);
  const create = useCreateAgent();
  const update = useUpdateAgent();
  const [s, setS] = useState<State>(() => initial(agent));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const set = <K extends keyof State>(k: K, v: State[K]) => setS((p) => ({ ...p, [k]: v }));
  const pending = create.isPending || update.isPending;

  const sensitiveAllowed = s.level !== "read";
  const effective = [
    ...(settings.data?.levels[s.level] ?? []),
    ...(sensitiveAllowed ? s.extra : []),
  ].sort();

  function validate(): Record<string, string> {
    const e: Record<string, string> = {};
    if (!s.name.trim() || s.name.trim().length > 100) e.name = t("agents.form.errName");
    if (!s.allProjects && s.projects.length === 0) e.project_ids = t("agents.form.errProjects");
    const d = Number(s.deletes);
    if (!Number.isInteger(d) || d < 0 || d > 10000) e.max_deletes_per_hour = "0–10000";
    return e;
  }

  async function submit(ev: React.FormEvent) {
    ev.preventDefault();
    const e = validate();
    setErrors(e);
    if (Object.keys(e).length) return;
    const body: AgentInput = {
      name: s.name.trim(),
      description: s.description.trim(),
      level: s.level,
      approval_mode: s.approval,
      project_ids: s.allProjects ? null : s.projects,
      allowed_ips: s.ips,
      max_deletes_per_hour: Number(s.deletes),
    };
    // Only send sensitive scopes when they change: non-owners may edit other fields.
    const extra = sensitiveAllowed ? s.extra : [];
    if (!agent || [...extra].sort().join() !== [...agent.extra_scopes].sort().join()) body.extra_scopes = extra;
    if (s.expiry !== "keep") body.expires_in_days = Number(s.expiry);
    try {
      if (agent) {
        await update.mutateAsync({ id: agent.id, body });
        onClose();
      } else {
        const res = await create.mutateAsync(body);
        onClose();
        onCreated(res);
      }
    } catch (err) {
      if (isApiError(err, "validation_failed") && Object.keys(err.fieldErrors).length) {
        setErrors(err.fieldErrors);
      } else {
        toast.error(agent ? t("agents.form.saveFailed") : t("agents.form.createFailed"), { description: errorMessage(err) });
      }
    }
  }

  const list = projects.data ?? [];

  return (
    <form onSubmit={submit} noValidate className="flex min-h-0 flex-1 flex-col">
      <SheetHeader>
        <SheetTitle>{agent ? t("agents.form.editTitle", { name: agent.name }) : t("agents.form.createTitle")}</SheetTitle>
        <SheetDescription>{t("agents.form.desc")}</SheetDescription>
      </SheetHeader>
      <div className="grid flex-1 gap-5 overflow-y-auto px-4 pb-4">
        <Field label={t("agents.form.name")} htmlFor="agent-name" error={errors.name}>
          <Input
            id="agent-name"
            value={s.name}
            placeholder={t("agents.form.namePh")}
            autoFocus
            aria-invalid={!!errors.name}
            onChange={(e) => set("name", e.target.value)}
          />
        </Field>
        <Field label={t("agents.form.description")} htmlFor="agent-desc" error={errors.description}>
          <Textarea
            id="agent-desc"
            rows={2}
            value={s.description}
            placeholder={t("agents.form.descriptionPh")}
            onChange={(e) => set("description", e.target.value)}
          />
        </Field>

        <fieldset className="grid gap-2">
          <legend className="mb-1.5 text-sm font-medium">{t("agents.form.level")}</legend>
          <div className="grid gap-1 rounded-lg border p-1" role="radiogroup">
            {AGENT_LEVELS.map((l) => (
              <label
                key={l}
                className={cn(
                  "flex cursor-pointer items-start gap-3 rounded-md px-2.5 py-2 hover:bg-accent/60",
                  s.level === l && "bg-accent/50",
                )}
              >
                <input
                  type="radio"
                  name="agent-level"
                  value={l}
                  checked={s.level === l}
                  onChange={() => set("level", l)}
                  className="mt-1 accent-[var(--color-brand)]"
                />
                <span className="grid gap-0.5">
                  <span className="text-sm font-medium">{t(LEVEL_LABEL[l])}</span>
                  <span className="text-xs text-muted-foreground">{t(LEVEL_DESC[l])}</span>
                </span>
              </label>
            ))}
          </div>
        </fieldset>

        <fieldset className="grid gap-2">
          <legend className="mb-1 text-sm font-medium">{t("agents.form.sensitive")}</legend>
          <p className="text-xs text-muted-foreground">
            {!sensitiveAllowed
              ? t("agents.form.sensitiveReadLevel")
              : !isOwner
                ? t("agents.form.sensitiveOwnerOnly")
                : t("agents.form.sensitiveHint")}
          </p>
          <div className="grid gap-1 rounded-lg border border-warning/30 p-1">
            {AGENT_SENSITIVE_SCOPES.map((sc) => {
              const checked = s.extra.includes(sc);
              const id = `scope-${sc}`;
              return (
                <label
                  key={sc}
                  htmlFor={id}
                  className={cn(
                    "flex items-start gap-3 rounded-md px-2.5 py-2",
                    sensitiveAllowed && isOwner ? "cursor-pointer hover:bg-accent/60" : "opacity-60",
                    checked && "bg-warning/10",
                  )}
                >
                  <Checkbox
                    id={id}
                    checked={checked && sensitiveAllowed}
                    disabled={!sensitiveAllowed || !isOwner}
                    className="mt-0.5"
                    onCheckedChange={(c) => set("extra", c ? [...s.extra, sc] : s.extra.filter((x) => x !== sc))}
                  />
                  <span className="grid gap-0.5">
                    <span className="flex items-center gap-1.5 font-mono text-xs font-medium">
                      {checked && sensitiveAllowed && <AlertTriangle className="size-3 text-warning" />}
                      {sc}
                    </span>
                    <span className="text-xs text-muted-foreground">{t(`agents.scope.${sc}` as MessageKey)}</span>
                  </span>
                </label>
              );
            })}
          </div>
          {errors.extra_scopes && (
            <p className="text-xs text-destructive" role="alert">
              {errors.extra_scopes}
            </p>
          )}
        </fieldset>

        <fieldset className="grid gap-2">
          <legend className="mb-1.5 text-sm font-medium">{t("agents.form.projects")}</legend>
          <label className="flex cursor-pointer items-start gap-2 text-sm">
            <input type="radio" name="agent-projects" checked={s.allProjects} onChange={() => set("allProjects", true)} className="mt-1" />
            <span>{t("agents.form.allProjects")}</span>
          </label>
          <label className="flex cursor-pointer items-center gap-2 text-sm">
            <input type="radio" name="agent-projects" checked={!s.allProjects} onChange={() => set("allProjects", false)} />
            <span>{t("agents.form.someProjects")}</span>
          </label>
          {!s.allProjects && (
            <div className="grid max-h-48 gap-0.5 overflow-y-auto rounded-lg border p-1">
              {list.map((p) => {
                const checked = s.projects.includes(p.id);
                return (
                  <label key={p.id} className="flex cursor-pointer items-center gap-2.5 rounded-md px-2 py-1.5 text-sm hover:bg-accent/60">
                    <Checkbox
                      checked={checked}
                      onCheckedChange={(c) => set("projects", c ? [...s.projects, p.id] : s.projects.filter((x) => x !== p.id))}
                    />
                    <span className="truncate">{p.name}</span>
                    <span className="ml-auto font-mono text-[11px] text-muted-foreground">{p.slug}</span>
                  </label>
                );
              })}
            </div>
          )}
          {errors.project_ids && (
            <p className="text-xs text-destructive" role="alert">
              {errors.project_ids}
            </p>
          )}
        </fieldset>

        <Field label={t("agents.form.approvalMode")} htmlFor="agent-approval" hint={t(APPROVAL_DESC[s.approval])} error={errors.approval_mode}>
          <Select value={s.approval} onValueChange={(v) => set("approval", v as AgentApprovalMode)}>
            <SelectTrigger id="agent-approval" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {AGENT_APPROVAL_MODES.map((m) => (
                <SelectItem key={m} value={m}>
                  {t(APPROVAL_LABEL[m])}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>

        <Field label={t("agents.form.allowedIps")} htmlFor="agent-ips" hint={t("agents.form.allowedIpsHint")} error={errors.allowed_ips}>
          <TagInput
            id="agent-ips"
            value={s.ips}
            onChange={(v) => set("ips", v)}
            placeholder="203.0.113.0/24"
            validate={(v) => (IP_RE.test(v) ? null : t("agents.form.ipInvalid"))}
          />
        </Field>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("agents.form.deletes")} htmlFor="agent-deletes" hint={t("agents.form.deletesHint")} error={errors.max_deletes_per_hour}>
            <Input
              id="agent-deletes"
              type="number"
              min={0}
              max={10000}
              value={s.deletes}
              onChange={(e) => set("deletes", e.target.value)}
            />
          </Field>
          <Field label={t("agents.form.expiry")} htmlFor="agent-expiry" error={errors.expires_in_days}>
            <Select value={s.expiry} onValueChange={(v) => set("expiry", v)}>
              <SelectTrigger id="agent-expiry" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {agent && (
                  <SelectItem value="keep">
                    {t("agents.form.expiryKeep", { when: agent.expires_at ? formatDate(agent.expires_at) : t("agents.never") })}
                  </SelectItem>
                )}
                {EXPIRY_DAYS.map((d) => (
                  <SelectItem key={d} value={String(d)}>
                    {t("agents.form.expiryDays", { count: d })}
                  </SelectItem>
                ))}
                <SelectItem value="0">{t("agents.form.expiryNever")}</SelectItem>
              </SelectContent>
            </Select>
          </Field>
        </div>

        {effective.length > 0 && (
          <div className="grid gap-1.5">
            <span className="text-sm font-medium">{t("agents.form.permissions")}</span>
            <div className="flex flex-wrap gap-1">
              {effective.map((p) => (
                <span key={p} className="rounded bg-secondary px-1.5 py-0.5 font-mono text-[11px]">
                  {p}
                </span>
              ))}
            </div>
          </div>
        )}
      </div>
      <SheetFooter className="flex-row justify-end border-t">
        <Button type="button" variant="outline" onClick={onClose}>
          {t("agents.form.cancel")}
        </Button>
        <Button type="submit" disabled={pending}>
          {pending && <Loader2 className="animate-spin" />}
          {agent ? t("agents.form.save") : t("agents.form.create")}
        </Button>
      </SheetFooter>
    </form>
  );
}
