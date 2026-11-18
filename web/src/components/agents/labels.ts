import type { MessageKey } from "@/i18n/dictionaries";
import type { AgentApprovalMode, AgentLevel } from "@/lib/types";

export const LEVEL_LABEL: Record<AgentLevel, MessageKey> = {
  read: "agents.level.read",
  edit: "agents.level.edit",
  full: "agents.level.full",
};
export const LEVEL_DESC: Record<AgentLevel, MessageKey> = {
  read: "agents.level.readDesc",
  edit: "agents.level.editDesc",
  full: "agents.level.fullDesc",
};
export const APPROVAL_LABEL: Record<AgentApprovalMode, MessageKey> = {
  none: "agents.approval.none",
  destructive: "agents.approval.destructive",
  writes: "agents.approval.writes",
};
export const APPROVAL_DESC: Record<AgentApprovalMode, MessageKey> = {
  none: "agents.approval.noneDesc",
  destructive: "agents.approval.destructiveDesc",
  writes: "agents.approval.writesDesc",
};

