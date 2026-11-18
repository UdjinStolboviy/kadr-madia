import { Link } from "@tanstack/react-router";
import { Bot, ChevronDown } from "lucide-react";
import { Fragment, useState } from "react";

import { RelativeTime } from "@/components/common";
import { TableCell, TableRow } from "@/components/ui/table";
import { useT } from "@/i18n/react";
import type { AuditEntry } from "@/lib/types";
import { cn } from "@/lib/utils";

function actionTone(action: string) {
  if (/(delete|revoke|disable|fail|denied|reject)/.test(action)) return "text-destructive";
  if (/(create|enable|approve)/.test(action)) return "text-success";
  if (/(login|logout|auth)/.test(action)) return "text-brand";
  return "text-foreground";
}

function hasDetails(d: unknown) {
  if (d === null || d === undefined || d === "") return false;
  if (typeof d === "object" && Object.keys(d as object).length === 0) return false;
  return true;
}

/** Reason and MCP tool an agent attached to its request (stored in details). */
function agentNote(d: unknown): { reason?: string; tool?: string } {
  if (!d || typeof d !== "object") return {};
  const o = d as Record<string, unknown>;
  return {
    reason: typeof o.reason === "string" ? o.reason : undefined,
    tool: typeof o.tool === "string" ? o.tool : undefined,
  };
}

export function AuditRow({ e, projectName, agentName }: { e: AuditEntry; projectName?: string; agentName?: string }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const expandable = hasDetails(e.details);
  const isAgent = e.actor_type === "agent";
  const note = isAgent ? agentNote(e.details) : {};
  return (
    <Fragment>
      <TableRow
        className={cn(expandable && "cursor-pointer")}
        onClick={() => expandable && setOpen((o) => !o)}
        aria-expanded={expandable ? open : undefined}
      >
        <TableCell className="pl-4 text-sm text-muted-foreground">
          <RelativeTime value={e.created_at} />
        </TableCell>
        <TableCell>
          <span className={cn("font-mono text-xs font-medium", actionTone(e.action))}>{e.action}</span>
          <div className="mt-0.5 truncate text-xs text-muted-foreground md:hidden">{e.target}</div>
          {note.reason && (
            <div className="mt-0.5 max-w-[28rem] truncate text-xs text-muted-foreground" title={note.reason}>
              {t("audit.reason")}: “{note.reason}”
            </div>
          )}
        </TableCell>
        <TableCell className="hidden max-w-[16rem] md:table-cell">
          <span className="block truncate font-mono text-xs" title={e.target ?? undefined}>
            {e.target ?? "—"}
          </span>
        </TableCell>
        <TableCell className="hidden text-sm lg:table-cell">
          {e.project_id ? (
            <Link
              to="/projects/$projectId"
              params={{ projectId: e.project_id }}
              className="hover:underline"
              onClick={(ev) => ev.stopPropagation()}
            >
              {projectName ?? e.project_id}
            </Link>
          ) : (
            <span className="text-muted-foreground">—</span>
          )}
        </TableCell>
        <TableCell className="hidden sm:table-cell">
          {isAgent ? (
            <div className="text-xs">
              <span className="inline-flex items-center gap-1 font-medium text-brand">
                <Bot className="size-3.5" />
                {agentName ?? (e.actor_id ? e.actor_id.slice(0, 14) : t("audit.agentUnknown"))}
              </span>
              {note.tool && (
                <div className="font-mono text-[11px] text-muted-foreground" title={t("audit.tool")}>
                  {note.tool}
                </div>
              )}
            </div>
          ) : (
            <div className="text-xs">
              <span className="capitalize">{e.actor_type}</span>
              {e.actor_id && <span className="ml-1 font-mono text-muted-foreground">{e.actor_id.slice(0, 10)}</span>}
            </div>
          )}
        </TableCell>
        <TableCell className="hidden font-mono text-xs text-muted-foreground xl:table-cell">{e.ip ?? "—"}</TableCell>
        <TableCell className="w-8 pr-4">
          {expandable && (
            <button
              type="button"
              className="rounded-sm text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
              aria-label={open ? t("audit.hideDetails") : t("audit.showDetails")}
              onClick={(ev) => {
                ev.stopPropagation();
                setOpen((o) => !o);
              }}
            >
              <ChevronDown className={cn("size-4 transition-transform", open && "rotate-180")} />
            </button>
          )}
        </TableCell>
      </TableRow>
      {open && (
        <TableRow className="bg-muted/30 hover:bg-muted/30">
          <TableCell colSpan={7} className="px-4">
            <pre className="max-h-64 overflow-auto font-mono text-xs leading-relaxed whitespace-pre-wrap">
              {JSON.stringify(e.details, null, 2)}
            </pre>
          </TableCell>
        </TableRow>
      )}
    </Fragment>
  );
}
