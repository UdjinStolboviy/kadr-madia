# ADR-0014: AI agent access through MCP on top of the Admin API

**Status:** Accepted

## Context
Operators want AI agents (Claude Code, Claude Desktop and other MCP clients) to manage Kadr: tune presets, moderate content, debug jobs and webhooks. Agents must get exactly the rights a human grants them (read, edit, delete), and every action must stay attributable and stoppable.

## Decision
- **A separate principal.** Agents use their own tokens (`agent_tokens`, `kd_agent_…`). They never use admin sessions or project API keys. A token has a level (`read` < `edit` < `full`), optional sensitive permissions (granted only by owners, never part of a level), an optional project list, an approval mode, an IP allowlist, a deletion budget and an expiry.
- **One permission model for humans and agents** (`internal/access`). Every Admin API route is declared in a table with exactly one action and a project-scope rule. A single `authorize` middleware enforces roles for sessions, and grants, project scope, reason, rate limits, approvals and the deletion budget for agents. A test fails when a route lacks an annotation.
- **MCP as a thin adapter.** `/mcp` (Streamable HTTP, stateless JSON, no external SDK) maps each tool to one Admin API route and dispatches it in-process with the agent's identity. No business logic or permission check lives in the MCP layer. `tools/list` is filtered by the token's permissions.
- **Human in the loop** as data. Held requests are stored encrypted in `agent_approvals`. On approval they are replayed through the same handler with permissions re-checked, and the result is kept for the agent.
- **Moderation is a recorded decision** (`asset_moderation` + `asset.moderated` webhook) that the product enforces. This is consistent with Kadr being off the read path ([ADR-0005](0005-out-of-read-path.md)).

## Consequences
- Agents can do everything a human admin can, but not more. A future Admin API endpoint becomes available to agents only by adding a route annotation, plus a tool spec if MCP access is wanted.
- The reason, MCP tool name and approval id go into the audit details, so the audit log shows why each change was made, not only what changed.
- Bearer tokens bypass CSRF by design (they are not ambient credentials). Cloudflare Access needs a service token or a path bypass for `/mcp`.
- Per-token rate limits and the deletion budget are in-memory (single process). This is acceptable for one node; a multi-node setup would need a shared store.

## Alternatives considered
- **Give agents admin sessions or reuse project API keys:** no separation of duties, no per-agent scoping, and the audit trail would be ambiguous.
- **Business logic in MCP tools:** two code paths that drift apart, with permission checks duplicated.
- **Official MCP Go SDK:** viable. Kadr only needs stateless tools/resources/prompts, so ~300 lines without dependencies keep the supply chain smaller. The code can be swapped for the SDK if streaming or sampling is ever needed.
- **OAuth 2.1 authorization for MCP:** planned for one-click connectors (claude.ai). Bearer tokens cover Claude Code, Desktop (via `kadr mcp-bridge`) and IDEs today.
