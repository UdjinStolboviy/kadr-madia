import type { useT } from "@/i18n/react";
import type { Agent, AgentApprovalMode, AgentInput, AgentLevel } from "@/lib/types";

export interface AgentState {
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

export function initialAgentState(
  agent?: Agent,
  level?: AgentLevel,
): AgentState {
  return {
    name: agent?.name ?? "",
    description: agent?.description ?? "",
    level: agent?.level ?? level ?? "read",
    extra: agent?.extra_scopes ?? [],
    allProjects: agent ? agent.project_ids === null : true,
    projects: agent?.project_ids ?? [],
    approval: agent?.approval_mode ?? "destructive",
    ips: agent?.allowed_ips ?? [],
    deletes: String(agent?.max_deletes_per_hour ?? 50),
    expiry: agent ? "keep" : "90",
  };
}

/** Client-side checks shared by the agent form and the OAuth consent page. */
export function validateAgentState(
  s: AgentState,
  t: ReturnType<typeof useT>,
): Record<string, string> {
  const e: Record<string, string> = {};
  if (!s.name.trim() || s.name.trim().length > 100)
    e.name = t("agents.form.errName");
  if (!s.allProjects && s.projects.length === 0)
    e.project_ids = t("agents.form.errProjects");
  const d = Number(s.deletes);
  if (!Number.isInteger(d) || d < 0 || d > 10000)
    e.max_deletes_per_hour = "0–10000";
  return e;
}

/** Request body for POST /agents, PATCH /agents/{id} and the OAuth approval. */
export function agentStateToInput(s: AgentState, agent?: Agent): AgentInput {
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
  const extra = s.level !== "read" ? s.extra : [];
  if (
    !agent ||
    [...extra].sort().join() !== [...agent.extra_scopes].sort().join()
  )
    body.extra_scopes = extra;
  if (s.expiry !== "keep") body.expires_in_days = Number(s.expiry);
  return body;
}

