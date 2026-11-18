# ADR-0015: AI operations — Claude moderation and scheduled agent runs inside Kadr

**Status:** Accepted

## Context
Operators want unattended AI work: moderate new uploads automatically, and run recurring reviews (nightly moderation, health and cost reports) without keeping an MCP client open. A local NSFW model would need an ML runtime (ONNX/Python) next to a single Go binary on a small LXC.

## Decision
- **Classifier interface** with two implementations: Claude vision via the official Anthropic Go SDK (structured JSON output, low effort, server-side refusal fallback) and an **HTTP classifier** for a self-hosted model. The binary stays free of ML runtimes; images stay local when the self-hosted one is configured.
- **Moderation as jobs** in a new pool `ai`: a minute tick queues unreviewed assets of projects with auto-moderation (only those created after enabling; backlog on demand); decisions are ordinary moderation records (`actor_type: system`), audited, with the webhook. `flag_only` mode by default: the AI never rejects on its own.
- **Scheduled runs reuse the agent model**: a schedule binds a prompt to an agent token; the run is a Claude tool-use loop over that token's MCP tools, dispatched in-process through the same authorization, approvals, budgets and audit as any agent. No separate permission system.
- The loop is written over the Messages API (not Managed Agents) because the tools are Kadr's own in-process endpoints, and runs must stay on the operator's infrastructure with Kadr's audit.

## Consequences
- AI is entirely optional (no `ANTHROPIC_API_KEY` → features report unavailable).
- Moderated previews and tool results leave the host when Claude is used; documented in SECURITY.md.
- Cost is bounded per run (turn limit, 30-minute timeout) and per asset (one low-effort vision call).

## Alternatives considered
- **Embedded ONNX NSFW model:** cgo + model files in the binary, poorer policy flexibility (fixed labels), retraining burden.
- **Managed Agents for scheduled runs:** Anthropic would host the loop, but Kadr's tools are internal endpoints that should not be exposed; the in-process loop keeps one audit trail.
