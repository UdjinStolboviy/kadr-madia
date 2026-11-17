/** Failure of an upload request. `retryable` tells whether another attempt may succeed. */
export class KadrUploadError extends Error {
  override name = "KadrUploadError";
  readonly status: number | undefined;
  readonly retryable: boolean;
  readonly partNumber: number | undefined;

  constructor(
    message: string,
    init: { status?: number; retryable?: boolean; partNumber?: number; cause?: unknown } = {},
  ) {
    super(message, init.cause === undefined ? undefined : { cause: init.cause });
    this.status = init.status;
    this.retryable = init.retryable ?? false;
    this.partNumber = init.partNumber;
  }
}

/** Thrown when the caller's AbortSignal fires. Named `AbortError` like DOM aborts. */
export class KadrAbortError extends Error {
  override name = "AbortError";
  constructor(message = "upload aborted") {
    super(message);
  }
}

export function throwIfAborted(signal: AbortSignal | undefined): void {
  if (signal?.aborted) throw new KadrAbortError();
}
