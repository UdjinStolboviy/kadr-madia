/**
 * Structural asset types. They accept both the wire format (`snake_case`, as
 * returned by the Kadr API) and the camelCase objects of `@kadr/node`, so you
 * can pass whichever your backend forwards, without importing either package.
 */
export interface KadrRendition {
  name: string;
  format: string;
  width?: number | null | undefined;
  height?: number | null | undefined;
  bytes?: number | undefined;
  /** Public renditions carry a URL. */
  url?: string | undefined;
  /** Private renditions carry a storage key to be signed by your backend; see `resolveUrl`. */
  key?: string | undefined;
}

export interface KadrPlaceholder {
  thumbhash?: string | null | undefined;
  dominant_color?: string | null | undefined;
  dominantColor?: string | null | undefined;
}

export interface KadrOriginal {
  width?: number | null | undefined;
  height?: number | null | undefined;
}

export interface KadrAssetLike {
  renditions: readonly KadrRendition[];
  placeholder?: KadrPlaceholder | null | undefined;
  original?: KadrOriginal | null | undefined;
  poster?: KadrRendition | null | undefined;
}

/** Maps a rendition to the URL to load, e.g. to sign private keys. Return undefined to skip it. */
export type ResolveUrl = (rendition: KadrRendition) => string | undefined;
