export interface FakeResponse {
  status?: number;
  etag?: string | null;
  network?: boolean;
}

export interface FakeRequest {
  url: string;
  headers: Record<string, string>;
  body: Blob;
}

/** Minimal scriptable XMLHttpRequest stand-in. `handler` decides each response. */
export function fakeXhrFactory(handler: (req: FakeRequest) => FakeResponse | Promise<FakeResponse>) {
  const requests: FakeRequest[] = [];
  let active = 0;
  let maxActive = 0;
  const create = (): XMLHttpRequest => {
    const headers: Record<string, string> = {};
    let url = "";
    let aborted = false;
    const x = {
      upload: {} as { onprogress?: (e: unknown) => void },
      status: 0,
      timeout: 0,
      open(_m: string, u: string) {
        url = u;
      },
      setRequestHeader(k: string, v: string) {
        headers[k] = v;
      },
      getResponseHeader: (): string | null => null,
      abort() {
        aborted = true;
        x.onabort?.();
      },
      onload: undefined as undefined | (() => void),
      onerror: undefined as undefined | (() => void),
      onabort: undefined as undefined | (() => void),
      ontimeout: undefined as undefined | (() => void),
      send(body: Blob) {
        const req = { url, headers, body };
        requests.push(req);
        active++;
        maxActive = Math.max(maxActive, active);
        void Promise.resolve(handler(req)).then((r) => {
          active--;
          if (aborted) return;
          if (r.network) return x.onerror?.();
          x.status = r.status ?? 200;
          x.getResponseHeader = () => (r.etag === undefined ? null : r.etag);
          x.upload.onprogress?.({ lengthComputable: true, loaded: body.size / 2, total: body.size });
          x.onload?.();
        });
      },
    };
    return x as unknown as XMLHttpRequest;
  };
  return { create, requests, maxActive: () => maxActive };
}

export const noSleep = (): Promise<void> => Promise.resolve();

export function readText(b: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const fr = new FileReader();
    fr.onload = () => {
      resolve(fr.result as string);
    };
    fr.onerror = () => {
      reject(new Error("read failed"));
    };
    fr.readAsText(b);
  });
}
