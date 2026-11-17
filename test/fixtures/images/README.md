# Image fixtures

Synthetic, license-free images for the image engine golden tests
(`internal/engine/image/golden_test.go`). Every file is generated locally by
[`generate.sh`](generate.sh) with libvips and ffmpeg; nothing is downloaded
and no real photos or documents are used.

Regenerate with:

```bash
test/fixtures/images/generate.sh
```

Requirements: `vips` (libvips with libheif and an HEVC encoder for the HEIC
sample), `ffmpeg` (lavfi sources `gradients`, `mandelbrot`, `testsrc2`;
filters `blend`, `noise`, `drawbox`, `geq`), and optionally `exiftool`.
Output differs slightly between tool versions; the golden tests assert
properties (budget, bounds, orientation, metadata, SSIM floor), not exact
bytes, so regenerated files keep passing.

| File | What it exercises | How it is generated |
|---|---|---|
| `photo.jpg` | Photo-like content: smooth gradients, soft structure, grain. 1600×1200. | ffmpeg `gradients` (4 colours) blended in overlay mode with a blurred `mandelbrot`, plus temporal noise; JPEG q≈3 (`-q:v 3`). |
| `texture.jpg` | High-detail worst case for a byte budget. 1000×1000. | Deep-zoom `mandelbrot` plus strong noise. |
| `screenshot.png` | Flat UI: large uniform areas, sharp edges, small text. 1280×800. | ffmpeg `color` + `drawbox` panels and a bar chart; text rendered with `vips text` (Pango) and pasted with `vips insert`. |
| `document.jpg` | Scanned document: dark text on gray paper with noise. 1680×2200. | `vips text` (monospace, 220 dpi) of a generic invoice for "Acme Supplies Ltd."; inverted onto gray paper (`linear`), Gaussian noise added, JPEG Q85. |
| `alpha.png` | RGBA with a soft-edged transparent background. 800×800. | ffmpeg `geq` with an alpha channel shaped as a disc. |
| `rotated.jpg` | EXIF orientation. Stored 1200×800 landscape with `Orientation=6`; displays as 800×1200 portrait. A red block sits in the stored top-left corner, so after correct autorotation it is in the top-right corner. | ffmpeg `gradients` + `drawbox`; orientation set with `exiftool -Orientation=6 -n` (fallback: libvips `orientation` metadata). |
| `sample.heic` | HEIF/HEVC input (the format phones produce). 1024×768. | `vips thumbnail photo.jpg 1024` then `vips heifsave --compression hevc --Q 50`. Skipped with a note when libheif has no HEVC encoder; the tests then skip this fixture. |
| `animated.gif` | Multi-page input; the engine must use the first page only. 320×240, 6 frames. | ffmpeg `testsrc2` with `palettegen`/`paletteuse`. |
| `corrupt.jpg` | Truncated JPEG (no EOI marker, scan data cut). | First 40% of `photo.jpg`. |
| `not-an-image.txt` | Unsupported media (magic-byte sniffing). | Plain text. |
