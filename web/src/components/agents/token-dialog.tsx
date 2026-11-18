import { AlertTriangle } from "lucide-react";
import { useState, type ReactNode } from "react";

import { CopyButton } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useT } from "@/i18n/react";
import type { AgentSecretResponse } from "@/lib/types";

function Block({ label, value, testId }: { label: ReactNode; value: string; testId?: string }) {
  const t = useT();
  return (
    <div className="grid gap-1.5">
      <div className="flex items-center justify-between gap-2">
        <span className="text-sm font-medium">{label}</span>
        <CopyButton value={value} size="sm" variant="outline" label={t("agents.token.copy")} />
      </div>
      <pre
        className="max-h-48 overflow-auto rounded-lg border bg-muted/50 p-2.5 font-mono text-xs break-all whitespace-pre-wrap select-all"
        data-testid={testId}
      >
        {value}
      </pre>
    </div>
  );
}

/** One-time reveal of an agent token with ready-to-paste MCP client setup. */
export function AgentTokenDialog({ result, onDone }: { result: AgentSecretResponse | null; onDone: () => void }) {
  const t = useT();
  const [copied, setCopied] = useState(false);
  return (
    <Dialog open={!!result} onOpenChange={() => undefined}>
      <DialogContent
        className="max-h-[92svh] overflow-y-auto sm:max-w-2xl"
        showCloseButton={false}
        onEscapeKeyDown={(e) => e.preventDefault()}
        onInteractOutside={(e) => e.preventDefault()}
      >
        {result && (
          <>
            <DialogHeader>
              <DialogTitle>{t("agents.token.title", { name: result.agent.name })}</DialogTitle>
              <DialogDescription>{t("agents.token.desc")}</DialogDescription>
            </DialogHeader>
            <div className="flex gap-3 rounded-lg border border-warning/40 bg-warning/10 p-3 text-sm">
              <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning" />
              <p>{t("agents.token.warning")}</p>
            </div>
            <div className="flex items-center gap-2 rounded-lg border bg-muted/50 p-2 pl-3">
              <code className="min-w-0 flex-1 font-mono text-xs break-all select-all" data-testid="agent-token">
                {result.token}
              </code>
              <CopyButton value={result.token} variant="default" size="sm" label={t("agents.token.copy")} onCopied={() => setCopied(true)} />
            </div>
            <Block label={t("agents.token.claudeCode")} value={result.mcp.claude_code_command} />
            <Block label={t("agents.token.json")} value={result.mcp.json_config} />
            <div className="text-xs text-muted-foreground">
              {t("agents.token.url")}: <code className="font-mono">{result.mcp.url}</code>
            </div>
            <DialogFooter>
              <Button
                onClick={() => {
                  setCopied(false);
                  onDone();
                }}
                variant={copied ? "default" : "outline"}
              >
                {copied ? t("agents.token.done") : t("agents.token.saved")}
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
