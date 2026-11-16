# ADR-0008: Eager renditions from versioned presets

**Status:** Accepted

## Context
On-the-fly transformations (as in Cloudinary/imgproxy) require a transformation server on the read path and keeping originals — which contradicts ADR-0004/0005 and the goal of minimal storage.

## Decision
- Processing generates every rendition defined by the preset version.
- Preset versions are immutable; changing parameters creates a new version; reprocessing is an explicit action (`/reprocess` or a bulk job from the admin console) that creates a new Blob with new URLs.
- The original is kept only when the preset has `retainOriginal: true`.

## Consequences
- Delivery is plain static files from the CDN, `immutable`.
- A new size for the frontend requires a new preset version + reprocessing (or keeping originals).
- Path to on-demand renditions (M5): lazy generation cached in R2 for presets that keep originals.
