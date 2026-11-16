# ADR-0007: libvips CLI instead of an imgproxy daemon

**Status:** Accepted

## Context
Options considered: imgproxy as a resident sidecar; govips/bimg (CGO bindings to libvips); invoking the `vips` CLI.

What matters for Kadr:
- renditions are generated at write time, not on the fly (ADR-0008);
- we need grayscale + normalise (DOCUMENT preset), smart crop and a controllable AVIF encoder;
- we want to keep `CGO_ENABLED=0`;
- imgproxy's `max_bytes` does not apply to every format, and some operations are Pro-only.

## Decision
An `ImageEngine` interface with a **VipsCLI** implementation: a sequence of `vips`/`vipsthumbnail`/`vipsheader` calls via `exec.CommandContext` with an intermediate `.v` file; budget encoding is implemented by Kadr (ADR-0009).

## Consequences
- 0 MB RAM at idle, the full libvips operation set, no CGO.
- Process spawn overhead of ~10–30 ms — negligible for asynchronous processing.
- The libvips version comes from the distribution (Debian 13: 8.16); `kadr doctor` verifies the AVIF encoder.
- The implementation can be swapped for govips or imgproxy without touching the pipeline.

## Alternatives
- imgproxy daemon — +40–60 MB resident, Pro features; worthwhile only for on-demand renditions (M5).
- govips — CGO, hard to cross-compile from a Mac.
