# ADR-0011: Webhooks via transactional outbox, Standard Webhooks

**Status:** Accepted

## Context
Products must reliably learn about `asset.ready` / `asset.failed`. Sending HTTP directly from the processor can lose an event (crash between commit and send) or announce a state that was never committed.

## Decision
- The event is written to `events` + `webhook_deliveries` in the same transaction as the state change.
- A dedicated delivery pool claims `pending` deliveries with a lease, sends with a 10 s timeout, retries for ~24 h, then marks them `dead`.
- The signature format follows the Standard Webhooks spec (`webhook-id`, `webhook-timestamp`, `webhook-signature`), compatible with existing verification libraries.

## Consequences
- At-least-once delivery; receivers are idempotent by `webhook-id`.
- Delivery log and manual re-send in the admin console.
