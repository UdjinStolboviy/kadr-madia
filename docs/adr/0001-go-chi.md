# ADR-0001: Go + chi

**Status:** Accepted

## Context
We need a service with minimal RAM usage, a single-file deployment that cross-compiles from macOS to Linux, and an embedded admin console. Candidates: Node.js/Fastify, Bun/Elysia, Go, Rust/Axum.

## Decision
Go 1.25 with the **chi v5** router on top of the standard `net/http`.

## Consequences
- `CGO_ENABLED=0 GOOS=linux go build` from a Mac in seconds; one static binary with the admin console embedded via `go:embed`.
- ~15–30 MB RAM at idle; goroutine worker pools without an external broker.
- Compatible with the `net/http` ecosystem (tusd, aws-sdk-go-v2, middleware) without adapters.
- Client SDKs remain in TypeScript (and other languages via OpenAPI), so integrators never need Go.

## Alternatives
- **Fiber** — built on fasthttp, incompatible with `net/http`; breaks streaming and middleware.
- **Rust/Axum** — saves ~20 MB RAM, but 90% of resources here are consumed by ffmpeg/libvips; cross-compiling from Apple Silicon is painful and development is 2–3× slower.
- **Node/Fastify, Bun** — 60–110 MB RAM, `node_modules`, harder to distribute as a self-hosted product.
