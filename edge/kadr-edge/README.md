# kadr-edge

Cloudflare Worker that serves private Kadr files from an R2 bucket behind
HMAC-signed URLs (`?exp=&kid=&sig=[&download=]`, or the directory-scoped
`prefix=1` mode for HLS). The signing algorithm is shared with the Go server
(`internal/pipeline/signing.go`) and the Node SDK (`sdk/node/src/signing.ts`).

Layout: `src/worker.ts` is the deployed entrypoint (default handler only),
`src/index.ts` holds the logic and helpers, `wrangler.toml` is the production
config, `wrangler.dev.toml` is the local-only config.

## Local dev

```sh
pnpm install
echo 'SIGNING_KEYS={"k3":"test-secret-0123456789abcdef"}' > .dev.vars   # git-ignored
pnpm dev            # wrangler dev -c wrangler.dev.toml --local  (workerd + local R2)
# seed an object into the local bucket
pnpm exec wrangler r2 object put kadr-private/acme/hello.txt --file ./hello.txt \
  --content-type text/plain --local -c wrangler.dev.toml
```

`wrangler.dev.toml` exists because `wrangler dev` cannot infer a host from the
`media.<domain>` placeholder route in `wrangler.toml`. No Cloudflare login is
needed in `--local` mode. Sign URLs with `signUrl()` from `sdk/node`.

## Tests

- `pnpm test` - fast unit tests (plain Node, mocked R2/Cache). Excludes integration tests.
- `pnpm test:integration` - spawns `wrangler dev --local` (real workerd, local
  miniflare R2 persisted to a temp dir, `SIGNING_KEYS` via `--var`), seeds a
  small, a 6 MiB (above the 5 MiB cache limit) and an avif object, and checks
  over HTTP with `fetch`. Takes about 75 s (mostly `wrangler r2 object put`
  seeding and workerd start-up). Needs no credentials or network beyond
  `pnpm install`.

## Deploy

1. `pnpm exec wrangler r2 bucket create kadr-private` (or edit `bucket_name`).
   The R2 binding name must stay `PRIVATE`.
2. Replace `media.<domain>` in `wrangler.toml` `routes` (custom domain on a zone in your account).
3. `pnpm exec wrangler secret put SIGNING_KEYS` with a JSON map of
   kid -> secret, e.g. `{"k3":"...","k2":"..."}` (active + verify-only kids;
   secrets are used as raw UTF-8 bytes).
4. Optional KV keys: `wrangler kv namespace create KEYS`, uncomment the
   `[[kv_namespaces]]` block (binding `KEYS`, key = kid, value = secret). KV is
   consulted before `SIGNING_KEYS`.
5. Optional `PATH_PREFIX` var if the host serves keys under a prefix.
6. `pnpm deploy`.

## What was verified locally (workerd, `--local`)

Valid signed URL (200, exact bytes, headers), URL from the Node SDK, signature
by the Go algorithm (cross-check vector from `signing_test.go` reproduced with
node crypto; its exp is in the past so the Worker rejects it as expired, and the
same algorithm with a far-future exp is accepted), tampered/foreign-secret/
wrong-key/wrong-download/malformed/missing sig, expired, unknown kid and
encoded `..` -> 403; missing object (GET/HEAD/Range) -> 404; POST/PUT/DELETE ->
405 + Allow; Range `0-99`, suffix, open-ended -> 206 + Content-Range; 416;
`If-None-Match` -> 304 (exact, list, `*`); `download` -> Content-Disposition;
HEAD; a 6 MiB object (full and ranged across the 5 MiB mark, byte-exact);
Cache API: repeated and ranged requests are consistent, a bad signature is
never served from a cached copy. Observed: the local Cache API **does** store
and serve entries (overwriting the R2 object behind the Worker still returned
the old bytes on the next request).

Bug found by running in workerd: the Worker failed to start because `index.ts`
exported non-handler names (`CACHE_MAX_BYTES`, `handle`, ...). Fixed by the
`src/worker.ts` entry shim (unit test: `test/worker-entry.test.ts`).

## Only verifiable on Cloudflare

Real R2 (consistency, latency, range semantics at scale, `httpEtag` format),
real Cache API behaviour (per-colo caching, eviction, `Cache-Control` /
`public` handling on a custom domain; the local cache is a simulation),
custom-domain routing/TLS, KV key lookup latency/propagation, and secret
distribution via `wrangler secret put`.
