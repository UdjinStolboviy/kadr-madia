import { KadrAbortError, KadrUploadError, throwIfAborted } from "./errors.js";
import type { RetryOptions } from "./types.js";

export function defaultSleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new KadrAbortError());
      return;
    }
    const onAbort = () => {
      clearTimeout(timer);
      reject(new KadrAbortError());
    };
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}

/** Exponential backoff with full jitter: random in [delay/2, delay]. */
export function backoffDelay(attempt: number, base: number, max: number, random = Math.random): number {
  const exp = Math.min(max, base * 2 ** attempt);
  return Math.round(exp / 2 + (random() * exp) / 2);
}

export function isRetryable(err: unknown): boolean {
  return err instanceof KadrUploadError && err.retryable;
}

/** Runs `fn`, retrying retryable KadrUploadErrors. `onRetry` runs before each retry (reset progress). */
export async function withRetry<T>(
  fn: (attempt: number) => Promise<T>,
  opts: RetryOptions & {
    signal?: AbortSignal | undefined;
    sleep?: ((ms: number, signal?: AbortSignal) => Promise<void>) | undefined;
    onRetry?: (() => void) | undefined;
  },
): Promise<T> {
  const retries = opts.retries ?? 3;
  const base = opts.baseDelayMs ?? 500;
  const max = opts.maxDelayMs ?? 8000;
  const sleep = opts.sleep ?? defaultSleep;
  for (let attempt = 0; ; attempt++) {
    throwIfAborted(opts.signal);
    try {
      return await fn(attempt);
    } catch (err) {
      if (err instanceof KadrAbortError || attempt >= retries || !isRetryable(err)) throw err;
      opts.onRetry?.();
      await sleep(backoffDelay(attempt, base, max), opts.signal);
    }
  }
}
