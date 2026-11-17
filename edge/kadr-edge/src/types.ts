// Minimal structural types for the Workers bindings we use, so the code and its
// tests run in plain Node without @cloudflare/workers-types.

export interface R2ObjectLike {
  key: string;
  size: number;
  httpEtag: string;
  uploaded: Date;
  httpMetadata?: { contentType?: string; contentLanguage?: string; contentEncoding?: string };
}
export interface R2ObjectBodyLike extends R2ObjectLike {
  body: ReadableStream<Uint8Array>;
}
export interface R2GetOptions {
  range?: { offset: number; length: number };
}
/** Read-only view of an R2 bucket: there is deliberately no put/delete/list. */
export interface R2ReadOnlyBucket {
  head(key: string): Promise<R2ObjectLike | null>;
  get(key: string, options?: R2GetOptions): Promise<R2ObjectBodyLike | null>;
}

export interface KVLike {
  get(key: string): Promise<string | null>;
}

export interface Env {
  PRIVATE: R2ReadOnlyBucket;
  /** Secret: JSON object mapping kid -> secret. */
  SIGNING_KEYS?: string;
  /** Optional KV namespace: key = kid, value = secret. */
  KEYS?: KVLike;
  /** Optional path prefix that is not part of the storage key. */
  PATH_PREFIX?: string;
}

export interface CacheLike {
  match(request: Request): Promise<Response | undefined>;
  put(request: Request, response: Response): Promise<void>;
}

export interface ExecutionContextLike {
  waitUntil(promise: Promise<unknown>): void;
}

export interface Deps {
  cache?: CacheLike | undefined;
  /** Current time in ms (tests). */
  now?: number;
}
