# ADR-0010: H.264 MP4 faststart by default, VAAPI auto-detection, HLS later

**Status:** Accepted

## Context
Most video in user-generated products is short clips (≤ 2–5 min). We need universal compatibility (iOS Safari, older Android), minimal size and instant start.

## Decision
- A single MP4: H.264 High, CRF + VBV ceiling (`maxrate`/`bufsize`), AAC, `+faststart`, no metadata; poster ≤ 15 KB.
- Hardware capabilities are detected at startup: if `/dev/dri` exists and a test encode succeeds, use `h264_vaapi`; otherwise `libx264 -preset slow -threads 2`.
- The video pool runs 1 concurrent job at S1.
- HLS ladder, AV1 and sprites come in M5 for long-form content.

## Consequences
- Plays everywhere, Range requests from R2/Worker, no HLS player needed.
- Long videos on slow connections do not adapt quality to bandwidth — acceptable for S1.
