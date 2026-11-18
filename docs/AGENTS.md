# AI agents (MCP)

Kadr can be managed by AI agents (Claude Code, Claude Desktop, Cursor and any other [Model Context Protocol](https://modelcontextprotocol.io) client): browse projects and assets, tune presets with the playground, moderate content, debug webhooks and jobs, and, if a human allows it, delete or issue keys.

An administrator decides what each agent may do in the admin console under **AI agents**. Every agent action goes through the same checks, rate limits and audit log as a human using the Admin API. Design decisions: [ADR 0014](adr/0014-agent-access-mcp.md).

## How it works

```
MCP client ──POST /mcp (JSON-RPC, Streamable HTTP)──► kadr admin listener
             Authorization: Bearer kd_agent_…            │
                                                         ▼
                                     agent token → kill switch → IP allowlist
                                                         ▼
                           tool → in-process Admin API request (as the agent)
                                                         ▼
           authorize: permission · project scope · reason · rate limits · approval · deletion budget
                                                         ▼
                                      handler → database → audit_log (actor_type = agent)
```

- **Agent tokens** (`kd_agent_<prefix>_<secret>`) are issued in the admin. The database stores only `SHA-256(secret ‖ pepper)`, and the secret is shown once.
- **The MCP endpoint** is `/mcp` on the admin listener (also served at `/admin/mcp`). It is stateless and returns JSON responses, with no SSE stream. Supported protocol versions are 2025-11-25, 2025-06-18, 2025-03-26 and 2024-11-05.
- **Tools never bypass the API.** Each tool call becomes an Admin API request carrying the agent's identity, so permissions are checked in one place.
- **Agents can also call the REST Admin API** directly with the same bearer token. Bearer tokens are not ambient credentials, so no CSRF token is needed.

## Permissions

Every Admin API route has exactly one *action*. A token has a **level**, plus optional **sensitive permissions** that an owner grants explicitly.

| Level | Actions | Typical use |
|---|---|---|
| `read` | `projects:read`, `assets:read`, `webhooks:read`, `jobs:read`, `analytics:read`, `keys:read` (metadata only), `storage:read` (masked), `system:read`, `audit:read` | reports, troubleshooting, cost review |
| `edit` | read + `projects:write`, `presets:write` (incl. playground), `webhooks:write`, `jobs:write`, `moderation:write` | preset tuning, moderation, operations |
| `full` | edit + `assets:delete`, `webhooks:delete`, `keys:revoke` | clean-up |

| Sensitive permission | Allows | Why it is separate |
|---|---|---|
| `keys:issue` | create API keys and URL signing keys | returns secrets to the agent |
| `storage:manage` | create / change / test / delete storage profiles | handles S3 credentials |
| `edge:publish` | read the edge config and publish keys to kadr-edge | handles signing secrets |
| `billing:manage` | change billing rates | money |
| `cloud:connect` | connect cloud drives | OAuth tokens |

Rules:
- Sensitive permissions require at least the `edit` level, and only an **owner** may grant them. Admins can issue tokens up to `full`. Viewers can see agents and approvals but cannot change them.
- Agents can never manage agents or approvals, flip the global switch, or touch anyone's sign-in (passwords, TOTP, passkeys).
- **Project scope:** a token can be limited to a list of projects. It then sees only those projects, including in the audit log. Instance-wide endpoints (jobs, storage profiles, project creation, billing rates, edge publishing) need a token valid for all projects. Resources addressed by id (webhooks, deliveries, API keys, signing keys, cloud connections) are resolved to their project and checked.

## Safety controls

| Control | Default | Behaviour |
|---|---|---|
| Reason | required | Every state-changing agent request needs a reason (3–500 characters): MCP argument `reason` or header `X-Kadr-Agent-Reason`. It is stored in the audit details along with the MCP tool name. |
| Approval mode | `destructive` | `none`: everything runs. `destructive`: deletions, revocations and sensitive actions are held. `writes`: every change is held. |
| Deletion budget | 50 / hour | `DELETE` requests per token per sliding hour → `429 delete_budget_exhausted`. `0` blocks deletions completely. |
| Rate limits | 300 req/min, 60 changes/min | per token → `429 rate_limited` with `Retry-After` |
| Expiry | 90 days | 1–365 days, or never |
| IP allowlist | any | CIDRs or single addresses → `403 ip_not_allowed` |
| Kill switch | on | **AI agents → Agent access** turns off every agent token at once → `403 agents_disabled` |
| Rotation / revocation | — | Rotation replaces the secret immediately. Revocation also rejects the token's pending approvals. |
| Denials | audited | Refused requests are logged as `agent.denied` with the missing permission. |

### Approvals (human in the loop)

When an action is held, the agent gets `202` back:

```json
{ "status": "pending_approval", "approval_id": "apr_…", "expires_at": "…", "detail": "…" }
```

An administrator reviews the request under **AI agents → Approvals**: agent, permission, method and path, reason and body. They approve or reject it, optionally with a note.

- **On approval**, Kadr runs the stored request as the agent. Permissions are checked again at that point, so a token whose rights were reduced in the meantime is refused. The outcome (HTTP status and body) is stored.
- **On expiry:** requests expire after 24 hours.
- **Duplicates:** an identical pending request is not queued twice.
- **The agent** polls `GET /admin/api/agent/approvals/{id}` (MCP tool `kadr_get_approval`) and sees the full result, e.g. an API key it asked for. Admin views mask secret-looking fields.
- **Encryption:** request bodies and results are encrypted at rest with the master key, because they can carry credentials.
- **Notification:** if Telegram alerting is configured, a message goes out for every new approval, with a link when `KADR_ADMIN_PUBLIC_URL` is set.

## Connecting a client

Create a token under **AI agents → New agent**. The dialog shows the token once, together with ready-to-paste configuration:

```bash
# Claude Code
claude mcp add --transport http kadr https://admin.example.com/mcp \
  --header "Authorization: Bearer kd_agent_XXXXXXXX_…"
```

```json
{
  "mcpServers": {
    "kadr": {
      "type": "http",
      "url": "https://admin.example.com/mcp",
      "headers": { "Authorization": "Bearer kd_agent_XXXXXXXX_…" }
    }
  }
}
```

For clients that only start local (stdio) servers, use the bridge built into the binary:

```json
{
  "mcpServers": {
    "kadr": {
      "command": "kadr",
      "args": ["mcp-bridge", "--url", "https://admin.example.com/mcp"],
      "env": { "KADR_AGENT_TOKEN": "kd_agent_…" }
    }
  }
}
```

### OAuth (claude.ai connectors and other OAuth-capable clients)

Kadr is also an OAuth 2.1 authorization server for MCP, so a client can connect without pasting a token:

1. Add a custom connector with the URL `https://admin.example.com/mcp` (claude.ai → Settings → Connectors, or any MCP client that supports OAuth).
2. The client discovers `/.well-known/oauth-protected-resource/mcp` and `/.well-known/oauth-authorization-server`, registers itself (`POST /oauth/register`, RFC 7591, public clients with PKCE S256) and opens `/oauth/authorize` in your browser.
3. You sign in to the admin. The consent screen shows the application, its redirect host and the requested scope (`kadr:read|edit|full`). You choose the level, projects, approval mode and the other limits — the same form as for manual tokens. **Approve** issues an agent token marked *OAuth* under AI agents; **Deny** returns `access_denied`.
4. `POST /oauth/token` exchanges the code (single use, 2 minutes, PKCE verified) for `access_token` (an agent token) and `refresh_token`. Each refresh rotates both. Revoking the agent in the admin invalidates both immediately.

Redirect URIs must be `https://…` (or `http://localhost` for native apps). For claude.ai the Kadr admin must be reachable from the internet over HTTPS (`KADR_ADMIN_PUBLIC_URL`), with `/mcp`, `/oauth/token`, `/oauth/register` and `/.well-known/` published past Cloudflare Access; `/oauth/authorize` and the consent page stay behind it.

### Cloudflare Access

The admin listener sits behind Cloudflare Access. Pick one option:
- **Service token (recommended):** create an Access *Service Token* and add a policy for it on the admin application (action *Service Auth*). The client then sends `CF-Access-Client-Id` and `CF-Access-Client-Secret` alongside the bearer token. With `kadr mcp-bridge`, pass `--cf-client-id` / `--cf-client-secret` (or `CF_ACCESS_CLIENT_ID` / `CF_ACCESS_CLIENT_SECRET`).
- **Bypass for the MCP path:** create a separate Access application for the path `/mcp` with a *Bypass* policy restricted to known IP ranges. The agent token then protects the endpoint on its own, so also set the token's IP allowlist.

Set `KADR_ADMIN_PUBLIC_URL=https://admin.example.com` so that setup snippets, OAuth metadata and notifications show the public URL.

**Origin protection.** With `KADR_CF_ACCESS_TEAM_DOMAIN` (e.g. `myteam.cloudflareaccess.com`) and `KADR_CF_ACCESS_AUD` (the application's AUD tag), Kadr verifies the `Cf-Access-Jwt-Assertion` header (RS256, team keys, issuer, audience, expiry) on every admin request and answers `403 access_required` otherwise. Exempt paths default to `/healthz,/readyz,/mcp,/admin/mcp,/oauth/token,/oauth/register,/.well-known/` (override with `KADR_CF_ACCESS_EXEMPT`); they must match the paths you publish with a Bypass policy.

## Tools

The tools a client sees are filtered by the token's permissions and project scope. A tool the token cannot use is not listed at all. Annotations (`readOnlyHint`, `destructiveHint`, `idempotentHint`) let clients ask the user before risky calls. Every changing tool has a required `reason` argument.

| Area | Tools |
|---|---|
| Identity | `kadr_whoami`, `kadr_list_my_approvals`, `kadr_get_approval` |
| Projects | `kadr_list_projects`, `kadr_get_project`, `kadr_create_project`, `kadr_update_project` |
| Presets | `kadr_list_presets`, `kadr_list_compression_modes`, `kadr_test_preset` (playground, no side effects), `kadr_save_preset`, `kadr_reset_preset` |
| Assets | `kadr_list_assets`, `kadr_get_asset`, `kadr_view_asset` (returns the image), `kadr_delete_asset` |
| Moderation | `kadr_moderation_queue`, `kadr_set_moderation` |
| Analytics | `kadr_get_analytics`, `kadr_get_billing`, `kadr_get_billing_rates`, `kadr_set_billing_rates` |
| Webhooks | `kadr_list_webhooks`, `kadr_list_deliveries`, `kadr_create_webhook`, `kadr_test_webhook`, `kadr_retry_delivery`, `kadr_delete_webhook` |
| Keys | `kadr_list_api_keys`, `kadr_create_api_key`, `kadr_revoke_api_key`, `kadr_list_signing_keys`, `kadr_create_signing_key`, `kadr_retire_signing_key`, `kadr_publish_edge_keys`, `kadr_revoke_cloud_connection` |
| Storage | `kadr_list_storage_profiles`, `kadr_create_storage_profile`, `kadr_test_storage_profile`, `kadr_delete_storage_profile` |
| Operations | `kadr_list_jobs`, `kadr_retry_job`, `kadr_cancel_job`, `kadr_get_system`, `kadr_list_audit` |

**Resources:** `kadr://guide/agents` (concepts and workflows) and `kadr://agent/permissions`.

**Prompts:** `moderate_assets`, `tune_presets`, `project_health`, `cost_review`.

## Moderation

Kadr is not on the read path, so moderation is a **decision that the product enforces**. Kadr does not block delivery itself.

- Each ready asset is `unreviewed` until someone records `approved`, `flagged` or `rejected`, with labels (e.g. `nsfw`, `violence`, `pii`) and a note.
- Every decision emits the **`asset.moderated`** webhook: `{ asset_id, namespace, status, previous_status, labels, note, actor_type }`. The product hides or removes the asset accordingly, or calls `DELETE /v1/assets/{id}`.
- `kadr_view_asset` returns a preview image: the image rendition, video poster or document thumbnail. AVIF is converted to WebP so vision models can read it.
- A sensible policy for agents: approve clear cases, flag anything doubtful for a human, never delete unless told to.

Admin API: `GET /projects/{id}/moderation?status=unreviewed|approved|flagged|rejected|all`, `GET|PUT /projects/{id}/assets/{asset_id}/moderation`, `GET /projects/{id}/assets/{asset_id}/preview?width=`.

## AI moderation (automatic)

Each project can have new assets moderated automatically (admin → Moderation → *Automatic AI moderation*):

- **Classifier:** Claude vision (`ANTHROPIC_API_KEY`, model `KADR_AI_MODEL`, default `claude-opus-5-5`) or a self-hosted model (`KADR_AI_CLASSIFIER_URL`, e.g. an open-source NSFW classifier behind a small HTTP service; request `{image_base64, mime_type, policy}`, response `{status, labels, note, confidence}`). The self-hosted classifier takes precedence for moderation when both are set — choose it when images must not leave your infrastructure.
- **Policy:** free text per project (a sensible default is filled in).
- **Mode:** `flag_only` (default) — the AI approves or flags, never rejects; `decide` — it may reject.
- **Scope:** kinds `image`, `video` (poster frame), `file` (document thumbnail). Only assets created after enabling are queued (every minute, pool `ai`); *Review backlog* queues up to 1000 existing unreviewed assets.
- Decisions are ordinary moderation records (`actor_type: system`, `actor_id: claude:<model>` or `http:<url>`), emit `asset.moderated`, are audited with the confidence, and never overwrite a decision a human or agent already made.
- Refusals by the model become `flagged` with label `ai_refused`. Metrics: `kadr_ai_moderations_total{status}`, `kadr_ai_moderation_errors_total`, `kadr_ai_moderation_seconds`.
- **Privacy:** with Claude, a preview (≤ 1024 px) of each moderated asset is sent to the Anthropic API. Mention it in your processing records; use the self-hosted classifier otherwise.

## Scheduled runs

Kadr can run Claude itself, on a schedule or on demand, with the MCP tools of an agent token (admin → AI agents → *Schedules*). Requires `ANTHROPIC_API_KEY`.

- A schedule = agent token + prompt + frequency (`hourly` at minute MM, `daily` at HH:MM UTC, `weekly` on a weekday) + max turns + Telegram report on/off.
- **The run has exactly the token's permissions:** its project scope, approval mode (held actions wait in Approvals and are listed in the report), deletion budget and rate limits. Tool calls are audited with the run id; the run itself as `agent.run`.
- Templates in the UI: nightly moderation, morning health report, weekly cost review. Give report-only schedules a `read` token.
- History: `GET /agent-runs` (status, report, tool calls, tokens, model). A run that cannot start (token revoked, agents switched off) fails with a reason. Runs are limited to 30 minutes and the configured turns.

## Prompt injection

File names, metadata, image content and webhook payloads come from end users. An agent that reads them and also holds delete rights is an attack surface. Kadr limits the damage:
- tools carry least privilege by default (`read`, `approval_mode=destructive`, a deletion budget);
- the MCP instructions and the `kadr_view_asset` result tell the model to treat that content as data;
- secrets are never returned except by explicitly granted sensitive actions;
- every change is attributable: actor, tool, reason, approval id.

Give moderation agents `edit` (they can flag, not delete) and keep `full` for supervised sessions.

## REST endpoints (Admin API)

| Method | Path | Who | Purpose |
|---|---|---|---|
| GET | `/admin/api/agents` | any admin | list tokens |
| POST | `/admin/api/agents` | owner/admin | issue → `201 { agent, token, mcp }` |
| GET / PATCH / DELETE | `/admin/api/agents/{id}` | view / owner/admin | read, change, revoke |
| POST | `/admin/api/agents/{id}/rotate` | owner/admin | new secret → `{ agent, token, mcp }` |
| GET / PUT | `/admin/api/agents/settings` | view / owner/admin | `{ enabled, pending_approvals, mcp_url, levels, sensitive_scopes }` |
| GET | `/admin/api/agent-approvals?status=&agent_id=` | any admin | queue and history |
| GET | `/admin/api/agent-approvals/{id}` | any admin | one request (masked) |
| POST | `/admin/api/agent-approvals/{id}/approve` · `/reject` | owner/admin | `{ note? }` |
| GET | `/admin/api/agent/me` | agent | own identity and permissions |
| GET | `/admin/api/agent/approvals[/{id}]` | agent | own held requests and their results |
| GET | `/admin/api/audit?actor_type=agent&actor_id=` | any | agent activity |
| GET / POST | `/admin/api/agent-schedules` | view / owner/admin | schedules (POST: `{name, agent_id, prompt, frequency, time_utc, weekday, max_turns, notify, enabled}`) |
| PATCH / DELETE | `/admin/api/agent-schedules/{id}` | owner/admin | change, remove |
| POST | `/admin/api/agent-schedules/{id}/run` | owner/admin | run now → `202 run` |
| GET / POST | `/admin/api/agent-runs` | view / owner/admin | history; POST `{agent_id, prompt}` starts an ad-hoc run |
| GET | `/admin/api/agent-runs/{id}` | any admin | one run with its report |
| GET / PUT | `/admin/api/projects/{id}/moderation/settings` | `assets:read` / `projects:write` | `{settings: {enabled, policy, mode, kinds, since}, ai}` |
| POST | `/admin/api/projects/{id}/moderation/backlog` | `moderation:write` | queue unreviewed assets → `202 {queued}` |
| GET | `/admin/api/ai/status` | `system:read` | `{available, classifier, agent_runs, model}` |
| GET | `/admin/api/oauth/grants/{id}` · POST `/approve` · `/deny` | owner/admin | OAuth consent |

Error codes: `insufficient_scope` (403), `project_not_allowed` (403), `agents_disabled` (403), `ip_not_allowed` (403), `reason_required` (422), `rate_limited` / `delete_budget_exhausted` (429), `not_pending` / `agent_inactive` (409).
