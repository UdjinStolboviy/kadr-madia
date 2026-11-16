import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { REGEXP_ONLY_DIGITS } from "input-otp";
import { Loader2, ShieldCheck, ShieldOff, Smartphone } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { encode } from "uqr";

import { CopyButton, Field, StatusBadge } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { InputOTP, InputOTPGroup, InputOTPSeparator, InputOTPSlot } from "@/components/ui/input-otp";
import { api, errorMessage, isApiError } from "@/lib/api";
import { formatDateTime } from "@/lib/format";
import { meQuery, qk } from "@/lib/queries";
import type { SessionResponse, TotpSetupResponse } from "@/lib/types";

function QrCode({ value, size = 184 }: { value: string; size?: number }) {
  const path = useMemo(() => {
    const qr = encode(value, { ecc: "M", border: 2 });
    let d = "";
    qr.data.forEach((row, y) =>
      row.forEach((on, x) => {
        if (on) d += `M${x} ${y}h1v1h-1z`;
      }),
    );
    return { d, n: qr.size };
  }, [value]);
  return (
    <svg
      role="img"
      aria-label="QR code for your authenticator app"
      viewBox={`0 0 ${path.n} ${path.n}`}
      width={size}
      height={size}
      shapeRendering="crispEdges"
      className="rounded-lg bg-white"
    >
      <path d={path.d} fill="#111" />
    </svg>
  );
}

function groupSecret(s: string) {
  return s.replace(/(.{4})/g, "$1 ").trim();
}

function setTotp(qc: ReturnType<typeof useQueryClient>, enabled: boolean) {
  qc.setQueryData<SessionResponse>(qk.me, (old) => (old ? { ...old, admin: { ...old.admin, totp_enabled: enabled } } : old));
}

function EnableDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const qc = useQueryClient();
  const [setup, setSetup] = useState<TotpSetupResponse | null>(null);
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);

  const start = useMutation({
    mutationFn: () => api.auth.totpSetup(),
    onSuccess: setSetup,
  });
  const enable = useMutation({
    mutationFn: (c: string) => api.auth.totpEnable(c),
    onSuccess: () => {
      setTotp(qc, true);
      void qc.invalidateQueries({ queryKey: qk.me });
      toast.success("Two-factor authentication enabled");
      onOpenChange(false);
    },
    onError: (e) => {
      setCode("");
      setError(isApiError(e, "invalid_code") ? "That code is not valid. Wait for a new one and try again." : errorMessage(e));
    },
  });

  // Request a fresh secret each time the dialog opens.
  const { isIdle, mutate: startSetup } = start;
  useEffect(() => {
    if (open && !setup && isIdle) startSetup();
  }, [open, setup, isIdle, startSetup]);

  function submit(c: string) {
    setError(null);
    if (c.length === 6 && !enable.isPending) enable.mutate(c);
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        onOpenChange(o);
        if (!o) {
          setSetup(null);
          setCode("");
          setError(null);
          start.reset();
        }
      }}
    >
      <DialogContent className="max-h-[92svh] overflow-y-auto sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Set up two-factor authentication</DialogTitle>
          <DialogDescription>
            Scan the QR code with an authenticator app (1Password, Google Authenticator, Authy…), then enter the
            6-digit code it shows.
          </DialogDescription>
        </DialogHeader>
        {start.isPending || (!setup && !start.isError) ? (
          <div className="flex h-56 items-center justify-center">
            <Loader2 className="size-5 animate-spin text-muted-foreground" />
          </div>
        ) : start.isError ? (
          <p className="text-sm text-destructive">{errorMessage(start.error)}</p>
        ) : setup ? (
          <form
            className="grid gap-5"
            onSubmit={(e) => {
              e.preventDefault();
              submit(code);
            }}
          >
            <div className="flex flex-col items-center gap-3">
              <div className="rounded-xl border p-2">
                <QrCode value={setup.otpauth_url} />
              </div>
              <div className="w-full text-center">
                <p className="mb-1 text-xs text-muted-foreground">Can't scan? Enter this key manually:</p>
                <div className="flex items-center justify-center gap-1">
                  <code className="font-mono text-sm tracking-wider break-all">{groupSecret(setup.secret)}</code>
                  <CopyButton value={setup.secret} label="Copy secret" size="icon-xs" />
                </div>
              </div>
            </div>
            <div className="grid justify-center gap-2">
              <InputOTP
                maxLength={6}
                pattern={REGEXP_ONLY_DIGITS}
                value={code}
                onChange={setCode}
                onComplete={submit}
                aria-label="Verification code"
                autoFocus
              >
                <InputOTPGroup>
                  <InputOTPSlot index={0} />
                  <InputOTPSlot index={1} />
                  <InputOTPSlot index={2} />
                </InputOTPGroup>
                <InputOTPSeparator />
                <InputOTPGroup>
                  <InputOTPSlot index={3} />
                  <InputOTPSlot index={4} />
                  <InputOTPSlot index={5} />
                </InputOTPGroup>
              </InputOTP>
              {error && (
                <p className="text-center text-xs text-destructive" role="alert">
                  {error}
                </p>
              )}
            </div>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={code.length !== 6 || enable.isPending}>
                {enable.isPending && <Loader2 className="animate-spin" />}
                Enable 2FA
              </Button>
            </DialogFooter>
          </form>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function DisableDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const qc = useQueryClient();
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const disable = useMutation({
    mutationFn: (p: string) => api.auth.totpDisable(p),
    onSuccess: () => {
      setTotp(qc, false);
      void qc.invalidateQueries({ queryKey: qk.me });
      toast.success("Two-factor authentication disabled");
      onOpenChange(false);
      setPassword("");
    },
    onError: (e) =>
      setError(isApiError(e, "invalid_credentials") ? "Incorrect password." : errorMessage(e)),
  });

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        onOpenChange(o);
        if (!o) {
          setPassword("");
          setError(null);
        }
      }}
    >
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Disable two-factor authentication?</DialogTitle>
          <DialogDescription>
            Your account will be protected by password only. Confirm with your current password.
          </DialogDescription>
        </DialogHeader>
        <form
          id="disable-totp"
          onSubmit={(e) => {
            e.preventDefault();
            setError(null);
            if (password) disable.mutate(password);
          }}
        >
          <Field label="Password" htmlFor="totp-pw" error={error ?? undefined}>
            <Input
              id="totp-pw"
              type="password"
              autoComplete="current-password"
              autoFocus
              value={password}
              aria-invalid={!!error}
              onChange={(e) => setPassword(e.target.value)}
            />
          </Field>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button type="submit" form="disable-totp" variant="destructive" disabled={!password || disable.isPending}>
            {disable.isPending && <Loader2 className="animate-spin" />}
            Disable 2FA
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function SecurityCard() {
  const me = useQuery(meQuery);
  const [enableOpen, setEnableOpen] = useState(false);
  const [disableOpen, setDisableOpen] = useState(false);
  const admin = me.data?.admin;
  const enabled = !!admin?.totp_enabled;

  return (
    <Card id="security" className="scroll-mt-20">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          Security
        </CardTitle>
        <CardDescription>{admin?.email}</CardDescription>
        <CardAction>
          {enabled ? (
            <StatusBadge tone="success">2FA on</StatusBadge>
          ) : (
            <StatusBadge tone="warning">2FA off</StatusBadge>
          )}
        </CardAction>
      </CardHeader>
      <CardContent className="grid gap-4">
        <div className="flex flex-col gap-4 rounded-lg border p-4 sm:flex-row sm:items-center">
          <div className="flex size-10 shrink-0 items-center justify-center rounded-full bg-muted">
            {enabled ? <ShieldCheck className="size-5 text-success" /> : <Smartphone className="size-5 text-muted-foreground" />}
          </div>
          <div className="flex-1 text-sm">
            <div className="font-medium">Authenticator app (TOTP)</div>
            <p className="text-muted-foreground">
              {enabled
                ? "A 6-digit code is required at every sign-in."
                : "Require a one-time code from your phone in addition to your password."}
            </p>
          </div>
          {enabled ? (
            <Button variant="outline" onClick={() => setDisableOpen(true)}>
              <ShieldOff /> Disable
            </Button>
          ) : (
            <Button onClick={() => setEnableOpen(true)}>
              <ShieldCheck /> Enable 2FA
            </Button>
          )}
        </div>
        {admin && (
          <p className="text-xs text-muted-foreground">
            Signed in as <span className="capitalize">{admin.role}</span> · last sign-in {formatDateTime(admin.last_login_at)}
          </p>
        )}
      </CardContent>
      <EnableDialog open={enableOpen} onOpenChange={setEnableOpen} />
      <DisableDialog open={disableOpen} onOpenChange={setDisableOpen} />
    </Card>
  );
}
