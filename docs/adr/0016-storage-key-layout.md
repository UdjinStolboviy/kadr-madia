# ADR-0016: Descriptive, partitioned storage keys

**Status:** Accepted

## Context
Every rendition was stored as `{project}/{blob}/{name}.{format}`, so a bucket listing showed thousands of `main.avif`, `main.webp` and `main.mp4` objects that could only be told apart through the database. Operators could not apply lifecycle rules or cost reports to "old videos", and a downloaded file said nothing about its size or codec. With AV1 next to H.264 and avif-only presets, the name also has to distinguish encodings of one role.

## Decision
- New blobs use `{project}/{kind}/{yyyy}/{mm}/{blob}/{file}` (originals: `originals/{project}/{kind}/{yyyy}/{mm}/{blob}.{ext}`; HLS under `…/{blob}/hls/`).
- `{file}` describes the object: `{name}-{w}x{h}.{format}` for images, posters and document thumbnails; `{name}-{height}p-{codec}.mp4` for video; `{name}.{format}` otherwise.
- The rendition **name** in the API (`main`, `poster`, `thumb`) and the signing contract (`"rendition": "main.avif"`) are unchanged; only keys differ.
- End-user file names are never part of a key (blobs are deduplicated across assets, names are personal data in public URLs, renames must not move objects); downloads get the name through `Content-Disposition`.
- Existing objects are not moved: rendition rows store full keys, visibility changes and GC work on stored keys.

## Consequences
- Buckets are browsable by project → kind → month; R2 lifecycle rules can target prefixes such as `omi-prod/video/2025/`.
- Clients that built keys themselves break; clients must use `url`/`key` from the API (always the documented contract).
- Two layouts coexist in old buckets until old assets expire.

## Alternatives considered
- **Original file name in the key** (`…/{blob}/holiday-photo.avif`): leaks personal data into CDN URLs and logs, conflicts with deduplication.
- **Namespace in the key:** blobs are shared across namespaces; purges already go through the database.
- **Migrating old objects:** copy + delete of every object for cosmetic gain, with cache invalidation; not worth the risk.
