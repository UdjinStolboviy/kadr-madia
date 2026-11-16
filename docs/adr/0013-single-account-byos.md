# ADR-0013: One legitimate platform storage account + BYOS

**Status:** Accepted

## Context
An idea was floated to combine several free-tier R2 accounts (10 GB each) and shard across them through Kadr.

## Decision
One platform Cloudflare account with billing and an alert. Customers and projects that need their own storage connect it as a BYOS profile (`storage_profiles.is_platform = 0`).

## Consequences
- No risk of all accounts being banned for ToS violations (and losing all media) to save ~$1–2/month.
- BYOS is the legitimate way to distribute storage — and a product feature for SaaS customers.
