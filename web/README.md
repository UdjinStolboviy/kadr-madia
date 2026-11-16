# Kadr admin UI

The admin console for Kadr. It's a static single-page app that the Go binary embeds and serves from the admin listener (`KADR_ADMIN_ADDR`, default `127.0.0.1:8081`). It talks only to the internal Admin API described in [`docs/ADMIN_API.md`](../docs/ADMIN_API.md).

**Stack:** Vite 8 · React 19 · TypeScript 6 (strict) · Tailwind CSS 4 · shadcn/ui (Radix) · TanStack Router + Query · react-hook-form + zod · lucide-react · sonner.

## Requirements

- Node.js ≥ 20 (tested with 24)
- pnpm ≥ 9

## Commands

```bash
pnpm install         # install dependencies (pnpm-lock.yaml is committed)
pnpm dev             # dev server on http://localhost:5173, proxies /admin/api → http://127.0.0.1:8081
pnpm dev:mock        # same, but with the in-memory mock API (no backend needed)
pnpm typecheck       # tsc --noEmit
pnpm lint            # eslint (flat config)
pnpm build           # typecheck + production build into web/dist
pnpm preview         # serve the production build locally
```

### Developing against the real backend

Start the backend in dev mode (`KADR_DEV=1`, so the session cookie is `kadr_session` without `Secure`), listening on `127.0.0.1:8081`. Then run `pnpm dev`. Vite proxies every `/admin/api/*` request, so cookies stay first-party.

### Mock mode

```bash
VITE_MOCK=1 pnpm dev     # or: pnpm dev:mock
```

Mock mode patches `window.fetch` for `/admin/api/*` with an in-memory implementation of the full M0 contract (`src/mocks/`). It covers sessions, CSRF checks, problem+json errors, the login rate limit, TOTP, `slug_taken`, `in_use` and connection tests. Data lives in `sessionStorage`, so it survives reloads but resets when you close the tab.

| What | Value |
|---|---|
| Email / password | `admin@example.com` / `kadr-admin` |
| TOTP code (after enabling 2FA) | `123456` |
| Seeded data | projects `acme`, `acme-staging`, `demo` (disabled); R2 + B2 storage profiles |
| Empty instance (onboarding flow) | open any URL with `?mock=empty` |
| Reset to seeded data | open any URL with `?mock=reset` |
| Failing connection test | any profile whose public base URL contains `archive` or `fail` |

The mock is loaded through a dynamic `import()` behind `import.meta.env.VITE_MOCK === "1"`. Vite replaces that check at build time, so a normal `pnpm build` contains no mock code. Don't set `VITE_MOCK` for release builds.

## Build output

`pnpm build` writes to `web/dist/`:

- `index.html`: SPA entry, served for `/` and any path outside `/admin/api`.
- `assets/*`: content-hashed JS/CSS/SVG, safe to serve with `Cache-Control: public, max-age=31536000, immutable`.

Routes are code-split. The vendor chunks (`react`, `tanstack`) change rarely, so browsers keep them cached across releases.

`index.html` has a tiny inline script that applies the saved dark/light theme before first paint. If the backend adds a strict `Content-Security-Policy`, allow it by hash (or move it to a file).

## Layout

```
src/
  main.tsx              bootstrap (providers, optional mock install, global 401 handler)
  router.tsx            code-based TanStack Router tree, auth guard, lazy routes
  lib/
    api.ts              typed fetch client: same-origin cookies, CSRF header, ApiError
    types.ts            types mirroring docs/ADMIN_API.md
    queries.ts          TanStack Query client, query options and mutations
    format.ts           bytes / dates / relative time / durations
    presets.ts          human-readable summaries of preset params
    forms.ts            shared form constants and validators
    theme.tsx           light / dark / system theme provider
  components/
    ui/                 shadcn/ui components (generated, lightly patched)
    app-shell.tsx       sidebar + top bar
    project/            project detail tabs (settings, API keys, presets)
    ...
  routes/               one lazily loaded module per screen
  mocks/                dev-only mock API (never in production builds)
```

To add a shadcn component, run `pnpm dlx shadcn@latest add <name>`, then make sure its `cn` import points at `@/lib/utils`.
