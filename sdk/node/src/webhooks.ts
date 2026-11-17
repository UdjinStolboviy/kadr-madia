import { createHmac, timingSafeEqual } from "node:crypto";
import { fromWire } from "./case.js";
import { WebhookVerificationError } from "./errors.js";
import type { WebhookEvent } from "./types.js";

/**
 * Webhook verification per the Standard Webhooks spec
 * (https://www.standardwebhooks.com/).
 *
 * Secret handling:
 * - `whsec_<base64>`: the prefix is stripped and the rest is base64-decoded
 *   into the HMAC key (the format Kadr issues).
 * - Any other string is used as the raw UTF-8 bytes of the secret.
 */

/** Default tolerance for `webhook-timestamp`, in seconds (both directions). */
export const WEBHOOK_TOLERANCE_SECONDS = 5 * 60;

export type RawBody = string | Uint8Array;

export type WebhookHeaders =
  | Headers
  | Record<string, string | string[] | undefined>;

export interface VerifyWebhookOptions {
  /** Current time in milliseconds (for tests). Defaults to `Date.now()`. */
  now?: number;
  /** Allowed clock skew in seconds. Defaults to 300. */
  toleranceSeconds?: number;
}

export interface SignWebhookInput {
  /** Value of the `webhook-id` header. */
  id: string;
  /** Unix time in seconds (value of `webhook-timestamp`). */
  timestamp: number;
  body: RawBody;
}

const PREFIX = "whsec_";

function secretKey(secret: string): Buffer {
  if (secret.startsWith(PREFIX)) {
    const key = Buffer.from(secret.slice(PREFIX.length), "base64");
    if (key.length === 0) throw new WebhookVerificationError("Invalid webhook secret");
    return key;
  }
  if (!secret) throw new WebhookVerificationError("Webhook secret must not be empty");
  return Buffer.from(secret, "utf8");
}

function bodyBytes(body: RawBody): Buffer {
  return typeof body === "string"
    ? Buffer.from(body, "utf8")
    : Buffer.from(body.buffer, body.byteOffset, body.byteLength);
}

function computeSignature(key: Buffer, id: string, timestamp: string, body: Buffer): Buffer {
  return createHmac("sha256", key).update(`${id}.${timestamp}.`, "utf8").update(body).digest();
}

/** Computes the `webhook-signature` header value (`v1,<base64>`) for a payload. */
export function signWebhook(secret: string, input: SignWebhookInput): string {
  const sig = computeSignature(
    secretKey(secret),
    input.id,
    String(input.timestamp),
    bodyBytes(input.body),
  );
  return `v1,${sig.toString("base64")}`;
}

function getHeader(headers: WebhookHeaders, name: string): string | undefined {
  if (headers instanceof Headers) return headers.get(name) ?? undefined;
  for (const [k, v] of Object.entries(headers)) {
    if (k.toLowerCase() !== name) continue;
    if (Array.isArray(v)) return v[0];
    return v;
  }
  return undefined;
}

/**
 * Verifies the signature and timestamp of a webhook delivery and returns the
 * parsed, camelCased event. Throws `WebhookVerificationError` on failure.
 *
 * Pass the raw request body exactly as received (before any JSON parsing).
 */
export function verifyWebhook(
  rawBody: RawBody,
  headers: WebhookHeaders,
  secret: string,
  opts: VerifyWebhookOptions = {},
): WebhookEvent {
  const id = getHeader(headers, "webhook-id");
  const timestamp = getHeader(headers, "webhook-timestamp");
  const signatureHeader = getHeader(headers, "webhook-signature");
  if (!id || !timestamp || !signatureHeader) {
    throw new WebhookVerificationError("Missing required webhook headers");
  }
  if (!/^\d+$/.test(timestamp)) {
    throw new WebhookVerificationError("Invalid webhook-timestamp header");
  }

  const nowSec = Math.floor((opts.now ?? Date.now()) / 1000);
  const tolerance = opts.toleranceSeconds ?? WEBHOOK_TOLERANCE_SECONDS;
  const ts = Number(timestamp);
  if (ts < nowSec - tolerance) throw new WebhookVerificationError("Message timestamp too old");
  if (ts > nowSec + tolerance) throw new WebhookVerificationError("Message timestamp too new");

  const body = bodyBytes(rawBody);
  const expected = Buffer.from(
    computeSignature(secretKey(secret), id, timestamp, body).toString("base64"),
    "utf8",
  );

  let matched = false;
  for (const entry of signatureHeader.split(" ")) {
    const comma = entry.indexOf(",");
    if (comma === -1) continue;
    const version = entry.slice(0, comma);
    if (version !== "v1") continue;
    // Compare the base64 text itself (not decoded bytes) so non-canonical
    // encodings are rejected, like the reference implementations do.
    const candidate = Buffer.from(entry.slice(comma + 1), "utf8");
    if (candidate.length === expected.length && timingSafeEqual(candidate, expected)) {
      matched = true;
      break;
    }
  }
  if (!matched) throw new WebhookVerificationError("No matching signature found");

  let payload: unknown;
  try {
    payload = JSON.parse(body.toString("utf8"));
  } catch {
    throw new WebhookVerificationError("Webhook payload is not valid JSON");
  }
  if (payload === null || typeof payload !== "object" || Array.isArray(payload)) {
    throw new WebhookVerificationError("Webhook payload must be a JSON object");
  }
  const event = fromWire<Record<string, unknown>>(payload);
  return { ...event, id } as unknown as WebhookEvent;
}

/** `kadr.webhooks` namespace. */
export class Webhooks {
  /** See {@link verifyWebhook}. */
  verify(
    rawBody: RawBody,
    headers: WebhookHeaders,
    secret: string,
    opts?: VerifyWebhookOptions,
  ): WebhookEvent {
    return verifyWebhook(rawBody, headers, secret, opts);
  }

  /** See {@link signWebhook}. */
  sign(secret: string, input: SignWebhookInput): string {
    return signWebhook(secret, input);
  }
}
