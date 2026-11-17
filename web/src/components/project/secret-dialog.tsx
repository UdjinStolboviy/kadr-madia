import { AlertTriangle } from "lucide-react";
import { useState, type ReactNode } from "react";

import { CopyButton } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useT } from "@/i18n/react";

/** One-time secret reveal (webhook signing secret, signing key secret). */
export function SecretDialog({
  open,
  onDone,
  title,
  description,
  secret,
  doneLabel,
}: {
  open: boolean;
  onDone: () => void;
  title: string;
  description: ReactNode;
  secret: string;
  doneLabel?: string;
}) {
  const t = useT();
  const [copied, setCopied] = useState(false);
  return (
    <Dialog open={open} onOpenChange={() => undefined}>
      <DialogContent showCloseButton={false} onEscapeKeyDown={(e) => e.preventDefault()} onInteractOutside={(e) => e.preventDefault()}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <div className="flex gap-3 rounded-lg border border-warning/40 bg-warning/10 p-3 text-sm">
          <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning" />
          <p>
            {t("signing.secretWarning")}
          </p>
        </div>
        <div className="flex items-center gap-2 rounded-lg border bg-muted/50 p-2 pl-3">
          <code className="min-w-0 flex-1 font-mono text-xs break-all select-all" data-testid="secret-value">
            {secret}
          </code>
          <CopyButton value={secret} variant="default" size="sm" label={t("signing.copy")} onCopied={() => setCopied(true)} />
        </div>
        <DialogFooter>
          <Button onClick={onDone} variant={copied ? "default" : "outline"}>
            {copied ? t("signing.done") : (doneLabel ?? t("signing.savedSecret"))}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
