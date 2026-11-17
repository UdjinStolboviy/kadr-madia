import { zodResolver } from "@hookform/resolvers/zod";
import { useQueryClient } from "@tanstack/react-query";
import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { REGEXP_ONLY_DIGITS } from "input-otp";
import { AlertCircle, ArrowLeft, KeyRound, Loader2, ShieldCheck } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";

import { Logo } from "@/components/app-shell";
import { Field } from "@/components/common";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { InputOTP, InputOTPGroup, InputOTPSeparator, InputOTPSlot } from "@/components/ui/input-otp";
import { api, errorMessage, isApiError } from "@/lib/api";
import { qk } from "@/lib/queries";
import { getCredential, isWebAuthnCancelled, webauthnSupported } from "@/lib/webauthn";

const route = getRouteApi("/login");

const schema = z.object({
  email: z.email("Enter a valid email address"),
  password: z.string().min(1, "Enter your password"),
});
type Values = z.infer<typeof schema>;

const RATE_LIMIT_SEC = 60;

export default function LoginPage() {
  const search = route.useSearch();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [step, setStep] = useState<"credentials" | "totp">("credentials");
  const [creds, setCreds] = useState<Values | null>(null);
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const [lockedFor, setLockedFor] = useState(0);
  const otpRef = useRef<HTMLInputElement>(null);

  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { email: "", password: "" } });

  // Focus the code field whenever the TOTP step is shown or a code is rejected.
  useEffect(() => {
    if (step === "totp" && !pending) otpRef.current?.focus();
  }, [step, pending]);

  useEffect(() => {
    if (lockedFor <= 0) return;
    const t = window.setTimeout(() => setLockedFor((s) => s - 1), 1000);
    return () => window.clearTimeout(t);
  }, [lockedFor]);

  async function attempt(values: Values, totp?: string) {
    setPending(true);
    setError(null);
    try {
      const session = await api.auth.login({ ...values, ...(totp ? { totp_code: totp } : {}) });
      qc.setQueryData(qk.me, session);
      await navigate({ to: search.redirect ?? "/", replace: true });
    } catch (e) {
      if (isApiError(e, "totp_required")) {
        setCreds(values);
        setStep("totp");
        setCode("");
      } else if (isApiError(e, "rate_limited")) {
        setLockedFor(RATE_LIMIT_SEC);
        setError(errorMessage(e));
      } else if (isApiError(e, "invalid_credentials") || isApiError(e, "invalid_code")) {
        setError(step === "totp" ? "That code didn't work. Check your authenticator and try again." : "Incorrect email or password.");
        if (step === "totp") setCode("");
      } else {
        setError(errorMessage(e));
      }
    } finally {
      setPending(false);
    }
  }

  async function passkeyLogin() {
    setPending(true);
    setError(null);
    try {
      const begin = await api.auth.passkeyLoginBegin();
      const credential = await getCredential(begin.options);
      const session = await api.auth.passkeyLoginFinish({ ceremony_id: begin.ceremony_id, credential });
      qc.setQueryData(qk.me, session);
      await navigate({ to: search.redirect ?? "/", replace: true });
    } catch (e) {
      if (isWebAuthnCancelled(e)) {
        // dismissed the browser prompt
      } else if (isApiError(e, "rate_limited")) {
        setLockedFor(RATE_LIMIT_SEC);
        setError(errorMessage(e));
      } else if (isApiError(e, "invalid_credentials")) {
        setError("Passkey sign-in failed. Use your password instead.");
      } else if (isApiError(e, "webauthn_disabled")) {
        setError("Passkeys are not configured on this server.");
      } else {
        setError(errorMessage(e));
      }
    } finally {
      setPending(false);
    }
  }

  const locked = lockedFor > 0;

  return (
    <div className="relative flex min-h-svh items-center justify-center overflow-hidden bg-background px-4 py-10">
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 bg-[radial-gradient(60rem_30rem_at_50%_-10%,color-mix(in_oklch,var(--brand)_14%,transparent),transparent)]"
      />
      <div className="relative w-full max-w-sm">
        <div className="mb-8 flex justify-center">
          <Logo />
        </div>
        <div className="rounded-xl border bg-card p-6 shadow-sm sm:p-8">
          {step === "credentials" ? (
            <form onSubmit={form.handleSubmit((v) => attempt(v))} noValidate className="grid gap-5">
              <div>
                <h1 className="text-lg font-semibold tracking-tight">Sign in to Kadr</h1>
                <p className="mt-1 text-sm text-muted-foreground">Admin console for your media platform.</p>
              </div>
              {error && (
                <Alert variant="destructive">
                  <AlertCircle />
                  <AlertDescription>
                    {error}
                    {locked && <span className="tabular-nums"> ({lockedFor}s)</span>}
                  </AlertDescription>
                </Alert>
              )}
              <Field label="Email" htmlFor="email" error={form.formState.errors.email?.message}>
                <Input
                  id="email"
                  type="email"
                  autoComplete="username"
                  autoFocus
                  aria-invalid={!!form.formState.errors.email}
                  {...form.register("email")}
                />
              </Field>
              <Field label="Password" htmlFor="password" error={form.formState.errors.password?.message}>
                <Input
                  id="password"
                  type="password"
                  autoComplete="current-password"
                  aria-invalid={!!form.formState.errors.password}
                  {...form.register("password")}
                />
              </Field>
              <Button type="submit" disabled={pending || locked} className="w-full">
                {pending && <Loader2 className="animate-spin" />}
                {locked ? `Try again in ${lockedFor}s` : "Sign in"}
              </Button>
              {webauthnSupported() && (
                <Button type="button" variant="outline" disabled={pending || locked} onClick={() => void passkeyLogin()} className="w-full">
                  <KeyRound /> Sign in with a passkey
                </Button>
              )}
            </form>
          ) : (
            <form
              className="grid gap-5"
              onSubmit={(e) => {
                e.preventDefault();
                if (creds && code.length === 6) void attempt(creds, code);
              }}
            >
              <div>
                <div className="mb-3 flex size-10 items-center justify-center rounded-full bg-brand/10 text-brand">
                  <ShieldCheck className="size-5" />
                </div>
                <h1 className="text-lg font-semibold tracking-tight">Two-factor authentication</h1>
                <p className="mt-1 text-sm text-muted-foreground">
                  Enter the 6-digit code from your authenticator app.
                </p>
              </div>
              {error && (
                <Alert variant="destructive">
                  <AlertCircle />
                  <AlertDescription>
                    {error}
                    {locked && <span className="tabular-nums"> ({lockedFor}s)</span>}
                  </AlertDescription>
                </Alert>
              )}
              <div className="flex justify-center">
                <InputOTP
                  ref={otpRef}
                  maxLength={6}
                  pattern={REGEXP_ONLY_DIGITS}
                  value={code}
                  onChange={setCode}
                  onComplete={(v: string) => creds && !locked && !pending && void attempt(creds, v)}
                  autoFocus
                  aria-label="Authentication code"
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
              </div>
              <Button type="submit" disabled={pending || locked || code.length !== 6} className="w-full">
                {pending && <Loader2 className="animate-spin" />}
                {locked ? `Try again in ${lockedFor}s` : "Verify"}
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => {
                  setStep("credentials");
                  setError(null);
                  setCode("");
                }}
              >
                <ArrowLeft /> Use a different account
              </Button>
            </form>
          )}
        </div>
        <p className="mt-6 text-center text-xs text-muted-foreground">
          Forgot your password? Reset it on the server with the <code className="font-mono">kadr admin</code> command.
        </p>
        {import.meta.env.VITE_MOCK === "1" && (
          <p className="mt-3 rounded-md border border-dashed px-3 py-2 text-center text-xs text-muted-foreground">
            Mock mode: <span className="font-mono">admin@example.com</span> /{" "}
            <span className="font-mono">kadr-admin</span>, TOTP <span className="font-mono">123456</span>
          </p>
        )}
      </div>
    </div>
  );
}
