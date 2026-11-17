import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { REGEXP_ONLY_DIGITS } from "input-otp";
import { KeyRound, Loader2, ShieldCheck, ShieldOff, Smartphone, Trash2 } from "lucide-react";
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
import { createCredential, isWebAuthnCancelled, webauthnSupported } from "@/lib/webauthn";
import { useT } from "@/i18n/react";

function QrCode({ value, size = 184 }: { value: string; size?: number }) {
  const t = useT();
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
      aria-label={t("security.qrAlt")}
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
  const t = useT();
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
      toast.success(t("security.enableDialog.enabledToast"));
      onOpenChange(false);
    },
    onError: (e) => {
      setCode("");
      setError(isApiError(e, "invalid_code") ? t("security.enableDialog.invalidCode") : errorMessage(e));
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
          <DialogTitle>{t("security.enableDialog.title")}</DialogTitle>
          <DialogDescription>{t("security.enableDialog.desc")}</DialogDescription>
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
                <p className="mb-1 text-xs text-muted-foreground">{t("security.enableDialog.cantScan")}</p>
                <div className="flex items-center justify-center gap-1">
                  <code className="font-mono text-sm tracking-wider break-all">{groupSecret(setup.secret)}</code>
                  <CopyButton value={setup.secret} label={t("security.enableDialog.copySecret")} size="icon-xs" />
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
                aria-label={t("security.enableDialog.codeLabel")}
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
                {t("security.cancel")}
              </Button>
              <Button type="submit" disabled={code.length !== 6 || enable.isPending}>
                {enable.isPending && <Loader2 className="animate-spin" />}
                {t("security.enable2fa")}
              </Button>
            </DialogFooter>
          </form>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function DisableDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const t = useT();
  const qc = useQueryClient();
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const disable = useMutation({
    mutationFn: (p: string) => api.auth.totpDisable(p),
    onSuccess: () => {
      setTotp(qc, false);
      void qc.invalidateQueries({ queryKey: qk.me });
      toast.success(t("security.disableDialog.disabledToast"));
      onOpenChange(false);
      setPassword("");
    },
    onError: (e) =>
      setError(isApiError(e, "invalid_credentials") ? t("security.disableDialog.wrongPassword") : errorMessage(e)),
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
          <DialogTitle>{t("security.disableDialog.title")}</DialogTitle>
          <DialogDescription>{t("security.disableDialog.desc")}</DialogDescription>
        </DialogHeader>
        <form
          id="disable-totp"
          onSubmit={(e) => {
            e.preventDefault();
            setError(null);
            if (password) disable.mutate(password);
          }}
        >
          <Field label={t("security.disableDialog.password")} htmlFor="totp-pw" error={error ?? undefined}>
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
            {t("security.cancel")}
          </Button>
          <Button type="submit" form="disable-totp" variant="destructive" disabled={!password || disable.isPending}>
            {disable.isPending && <Loader2 className="animate-spin" />}
            {t("security.disableDialog.submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function PasskeysSection() {
  const t = useT();
  const qc = useQueryClient();
  const list = useQuery({ queryKey: qk.passkeys, queryFn: () => api.auth.passkeys() });
  const [name, setName] = useState("");
  const supported = webauthnSupported();

  const register = useMutation({
    mutationFn: async (n: string) => {
      const begin = await api.auth.passkeyRegisterBegin();
      const credential = await createCredential(begin.options);
      return api.auth.passkeyRegisterFinish({ ceremony_id: begin.ceremony_id, name: n, credential });
    },
    onSuccess: () => {
      setName("");
      void qc.invalidateQueries({ queryKey: qk.passkeys });
      toast.success(t("security.passkeys.addedToast"));
    },
    onError: (e) => {
      if (isWebAuthnCancelled(e)) return;
      toast.error(isApiError(e, "webauthn_disabled") ? t("security.passkeys.notConfigured") : errorMessage(e));
    },
  });
  const remove = useMutation({
    mutationFn: (id: string) => api.auth.passkeyDelete(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.passkeys });
      toast.success(t("security.passkeys.removedToast"));
    },
    onError: (e) => toast.error(errorMessage(e)),
  });

  const items = list.data?.data ?? [];
  return (
    <div className="grid gap-3 rounded-lg border p-4">
      <div className="flex items-center gap-3">
        <div className="flex size-10 shrink-0 items-center justify-center rounded-full bg-muted">
          <KeyRound className="size-5 text-muted-foreground" />
        </div>
        <div className="flex-1 text-sm">
          <div className="font-medium">{t("security.passkeys.title")}</div>
          <p className="text-muted-foreground">{t("security.passkeys.desc")}</p>
        </div>
      </div>
      {items.length > 0 && (
        <ul className="divide-y rounded-md border">
          {items.map((k) => (
            <li key={k.id} className="flex items-center gap-3 px-3 py-2 text-sm">
              <div className="min-w-0 flex-1">
                <div className="truncate font-medium">{k.name}</div>
                <div className="text-xs text-muted-foreground">
                  {t("security.passkeys.added", { date: formatDateTime(k.created_at) })} ·{" "}
                  {k.last_used_at ? t("security.passkeys.lastUsed", { date: formatDateTime(k.last_used_at) }) : t("security.passkeys.neverUsed")}
                </div>
              </div>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={t("security.passkeys.delete", { name: k.name })}
                disabled={remove.isPending}
                onClick={() => {
                  if (window.confirm(t("security.passkeys.confirmDelete", { name: k.name }))) remove.mutate(k.id);
                }}
              >
                <Trash2 />
              </Button>
            </li>
          ))}
        </ul>
      )}
      {supported ? (
        <form
          className="flex flex-col gap-2 sm:flex-row"
          onSubmit={(e) => {
            e.preventDefault();
            if (name.trim()) register.mutate(name.trim());
          }}
        >
          <Input
            aria-label={t("security.passkeys.nameLabel")}
            placeholder={t("security.passkeys.namePlaceholder")}
            value={name}
            maxLength={80}
            onChange={(e) => setName(e.target.value)}
          />
          <Button type="submit" disabled={!name.trim() || register.isPending}>
            {register.isPending ? <Loader2 className="animate-spin" /> : <KeyRound />} {t("security.passkeys.add")}
          </Button>
        </form>
      ) : (
        <p className="text-xs text-muted-foreground">{t("security.passkeys.unsupported")}</p>
      )}
    </div>
  );
}

export function SecurityCard() {
  const t = useT();
  const me = useQuery(meQuery);
  const [enableOpen, setEnableOpen] = useState(false);
  const [disableOpen, setDisableOpen] = useState(false);
  const admin = me.data?.admin;
  const enabled = !!admin?.totp_enabled;

  return (
    <Card id="security" className="scroll-mt-20">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">{t("security.title")}</CardTitle>
        <CardDescription>{admin?.email}</CardDescription>
        <CardAction>
          {enabled ? (
            <StatusBadge tone="success">{t("security.twoFaOn")}</StatusBadge>
          ) : (
            <StatusBadge tone="warning">{t("security.twoFaOff")}</StatusBadge>
          )}
        </CardAction>
      </CardHeader>
      <CardContent className="grid gap-4">
        <div className="flex flex-col gap-4 rounded-lg border p-4 sm:flex-row sm:items-center">
          <div className="flex size-10 shrink-0 items-center justify-center rounded-full bg-muted">
            {enabled ? <ShieldCheck className="size-5 text-success" /> : <Smartphone className="size-5 text-muted-foreground" />}
          </div>
          <div className="flex-1 text-sm">
            <div className="font-medium">{t("security.totp.title")}</div>
            <p className="text-muted-foreground">
              {enabled
                ? t("security.totp.requiredAtLogin")
                : t("security.totp.promo")}
            </p>
          </div>
          {enabled ? (
            <Button variant="outline" onClick={() => setDisableOpen(true)}>
              <ShieldOff /> {t("security.disable")}
            </Button>
          ) : (
            <Button onClick={() => setEnableOpen(true)}>
              <ShieldCheck /> {t("security.enable2fa")}
            </Button>
          )}
        </div>
        <PasskeysSection />
        {admin && (
          <p className="text-xs text-muted-foreground">
            {t("security.signedInAs", { role: admin.role.charAt(0).toUpperCase() + admin.role.slice(1), when: formatDateTime(admin.last_login_at) })}
          </p>
        )}
      </CardContent>
      <EnableDialog open={enableOpen} onOpenChange={setEnableOpen} />
      <DisableDialog open={disableOpen} onOpenChange={setDisableOpen} />
    </Card>
  );
}
