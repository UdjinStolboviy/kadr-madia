/**
 * Harness for the contract tests: talks to a Kadr server started by
 * scripts/e2e-sdk.sh (KADR_DEV=1, --dev-storage, --fake-engine) and provides
 * a local webhook receiver.
 */
import { createServer, type IncomingHttpHeaders, type Server } from "node:http";
import type { AddressInfo } from "node:net";

export interface E2EConfig {
  publicUrl: string;
  adminUrl: string;
  adminEmail: string;
  adminPassword: string;
}

function env(name: string): string {
  const v = process.env[name];
  if (!v) throw new Error(`${name} is not set; run the e2e tests via scripts/e2e-sdk.sh`);
  return v;
}

export function loadConfig(): E2EConfig {
  return {
    publicUrl: env("KADR_E2E_PUBLIC_URL"),
    adminUrl: env("KADR_E2E_ADMIN_URL"),
    adminEmail: env("KADR_E2E_ADMIN_EMAIL"),
    adminPassword: env("KADR_E2E_ADMIN_PASSWORD"),
  };
}

/** Minimal client for the admin API (cookie session + X-CSRF-Token). */
export class AdminClient {
  private cookie = "";
  private csrf = "";

  constructor(private readonly baseUrl: string) {}

  async login(email: string, password: string): Promise<void> {
    const res = await fetch(`${this.baseUrl}/admin/api/auth/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email, password }),
    });
    if (res.status !== 200) throw new Error(`admin login: HTTP ${res.status} ${await res.text()}`);
    const session = res.headers
      .getSetCookie()
      .map((c) => c.split(";")[0] ?? "")
      .find((c) => c.includes("="));
    if (!session) throw new Error("admin login: no session cookie");
    this.cookie = session;
    const body = (await res.json()) as { csrf_token: string };
    this.csrf = body.csrf_token;
  }

  async post<T>(path: string, body: unknown): Promise<T> {
    const res = await fetch(`${this.baseUrl}/admin/api${path}`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Cookie: this.cookie, "X-CSRF-Token": this.csrf },
      body: JSON.stringify(body),
    });
    const text = await res.text();
    if (res.status >= 300) throw new Error(`admin POST ${path}: HTTP ${res.status} ${text}`);
    return JSON.parse(text) as T;
  }
}

export interface ReceivedWebhook {
  headers: IncomingHttpHeaders;
  /** Raw body exactly as received. */
  body: Buffer;
  type: string;
}

/** Collects webhook deliveries on 127.0.0.1 and answers 204. */
export class WebhookReceiver {
  readonly received: ReceivedWebhook[] = [];
  private server: Server | undefined;
  private waiters: (() => void)[] = [];

  async start(): Promise<string> {
    this.server = createServer((req, res) => {
      const chunks: Buffer[] = [];
      req.on("data", (c: Buffer) => chunks.push(c));
      req.on("end", () => {
        const body = Buffer.concat(chunks);
        let type = "";
        try {
          type = (JSON.parse(body.toString("utf8")) as { type?: string }).type ?? "";
        } catch {
          // keep empty type; the test will fail on verification
        }
        this.received.push({ headers: req.headers, body, type });
        res.statusCode = 204;
        res.end();
        for (const w of this.waiters.splice(0)) w();
      });
    });
    const server = this.server;
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const { port } = server.address() as AddressInfo;
    return `http://127.0.0.1:${port}/hooks`;
  }

  /** Resolves with the first delivery matching `pred`, or rejects after `timeoutMs`. */
  async waitFor(pred: (w: ReceivedWebhook) => boolean, timeoutMs = 20_000): Promise<ReceivedWebhook> {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const hit = this.received.find(pred);
      if (hit) return hit;
      const left = deadline - Date.now();
      if (left <= 0) {
        const types = this.received.map((w) => w.type).join(", ");
        throw new Error(`webhook not received within ${timeoutMs} ms (got: ${types || "none"})`);
      }
      await new Promise<void>((resolve) => {
        const t = setTimeout(resolve, Math.min(left, 500));
        this.waiters.push(() => {
          clearTimeout(t);
          resolve();
        });
      });
    }
  }

  async stop(): Promise<void> {
    const server = this.server;
    if (!server) return;
    server.closeAllConnections();
    await new Promise<void>((resolve) => {
      server.close(() => {
        resolve();
      });
    });
  }
}
