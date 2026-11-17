# @kadr/react

React components for **Kadr** assets. `react` (>= 18) is a peer dependency.

- `<KadrImage>`: `<picture>` with AVIF and WebP sources, `width`/`height` and an `aspect-ratio` wrapper (no layout shift), lazy loading, and a ThumbHash placeholder decoded to a PNG data URL and blurred until the image loads
- `<KadrVideo>`: `<video>` with mp4 sources and a poster; falls back to the ThumbHash as the poster
- A small dependency-free ThumbHash decoder (port of the reference algorithm; reads the base64 hash from `asset.placeholder.thumbhash`)
- Works with SSR (no canvas, no effects needed to render the placeholder)

Not published yet (`"private": true`); use it from this repository.

## Usage

```tsx
import { KadrImage, KadrVideo } from "@kadr/react";

<KadrImage asset={asset} alt="Front of the receipt" sizes="(max-width: 640px) 100vw, 400px" />
<KadrImage asset={hero} alt="" priority />          // eager + fetchpriority=high
<KadrVideo asset={clip} controls />
```

`asset` is the object your backend returns (`asset` from the API, either the
snake_case wire format or the camelCase `@kadr/node` objects). Only
`renditions`, `placeholder`, `original` and `poster` are read.

### Props

`KadrImage`: `asset`, `alt` (required), `sizes`, `placeholder` (default `true`),
`priority`, `resolveUrl`, `wrapperClassName`, `wrapperStyle`, plus any `<img>`
attribute (`className`, `style` and `onLoad` apply to the `<img>`).

`KadrVideo`: `asset`, `resolveUrl`, plus any `<video>` attribute (`preload`
defaults to `metadata`, `playsInline` to `true`).

Renditions are grouped by format; each format becomes a `<source>` whose
`srcset` lists the renditions with `w` descriptors. The `<img>` fallback is the
widest rendition of the most compatible format.

### Private assets

Private renditions carry a `key` instead of a `url`. Have your backend sign
them (`kadr.signUrl`) and either send the signed URLs along or map them:

```tsx
<KadrImage asset={asset} alt="" resolveUrl={(r) => signed[r.key!]} />
```

Renditions for which `resolveUrl` returns `undefined` are skipped.

### ThumbHash helpers

```ts
import { thumbHashToDataURL } from "@kadr/react";
const dataUrl = thumbHashToDataURL(asset.placeholder.thumbhash); // data:image/png;base64,...
```

Also exported: `thumbHashToRGBA`, `thumbHashToApproximateAspectRatio`,
`safeThumbHashToDataURL` (returns `undefined` on bad input), `planImage`, `planVideo`.

## Development

```sh
pnpm install
pnpm typecheck && pnpm lint && pnpm test && pnpm build
```
