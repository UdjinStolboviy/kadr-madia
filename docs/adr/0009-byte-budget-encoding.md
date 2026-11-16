# ADR-0009: Byte-budget encoding — quality search + downscaling

**Status:** Accepted

## Context
A core product promise is photos ≤ 20 KB that stay sharp. A fixed quality gives no size guarantee; aggressively lowering quality at a high resolution produces mush and blocking.

## Decision
Binary search for the highest quality that fits the budget (≤ 5 attempts) at the current size; if even `qMin` does not fit, shrink the long side by `downscaleStep` (0.85) and repeat, down to `minSide`. If the budget is unreachable, return the `minSide/qMin` result with an `over_budget` flag. Preset modes: `budget`, `fixed`, `ssim` (perceptual quality target).

## Consequences
- Guaranteed size and the best achievable quality within the budget.
- 3–10 encodes of a small raster (~20–60 ms each) — acceptable.
- Metrics: chosen `quality`, iteration count, `over_budget` share → tuning hints in the admin console.
