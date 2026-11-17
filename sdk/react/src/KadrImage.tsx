import { useEffect, useMemo, useRef, useState } from "react";
import type { CSSProperties, ImgHTMLAttributes, ReactElement } from "react";
import { planImage } from "./renditions.js";
import { safeThumbHashToDataURL } from "./thumbhash.js";
import type { KadrAssetLike, ResolveUrl } from "./types.js";

export interface KadrImageProps
  extends Omit<ImgHTMLAttributes<HTMLImageElement>, "src" | "srcSet" | "width" | "height" | "children"> {
  asset: KadrAssetLike;
  /** Passed to every `<source>` and the `<img>`, e.g. "(max-width: 640px) 100vw, 400px". */
  sizes?: string;
  alt: string;
  /** Show a blurred ThumbHash (or dominant color) until the image loads (default true). */
  placeholder?: boolean;
  /** Eager load with high fetch priority, for above-the-fold images. Default lazy. */
  priority?: boolean;
  /** Sign/resolve private renditions. */
  resolveUrl?: ResolveUrl;
  /** Class for the wrapper element. */
  wrapperClassName?: string;
  wrapperStyle?: CSSProperties;
}

const fill: CSSProperties = { position: "absolute", inset: 0, width: "100%", height: "100%" };

export function KadrImage(props: KadrImageProps): ReactElement {
  const {
    asset,
    sizes,
    alt,
    placeholder = true,
    priority = false,
    resolveUrl,
    wrapperClassName,
    wrapperStyle,
    style,
    onLoad,
    loading,
    ...rest
  } = props;
  const plan = useMemo(() => planImage(asset, resolveUrl), [asset, resolveUrl]);
  const hash = asset.placeholder?.thumbhash;
  const dataUrl = useMemo(() => (placeholder ? safeThumbHashToDataURL(hash) : undefined), [placeholder, hash]);
  const dominant = asset.placeholder?.dominant_color ?? asset.placeholder?.dominantColor ?? undefined;

  const [loaded, setLoaded] = useState(false);
  const imgRef = useRef<HTMLImageElement>(null);
  // The image may finish loading (cache, SSR) before React attaches onLoad.
  useEffect(() => {
    if (imgRef.current?.complete && imgRef.current.naturalWidth > 0) setLoaded(true);
  }, [plan.src]);
  useEffect(() => {
    setLoaded(false);
  }, [plan.src]);

  const showPlaceholder = placeholder && !loaded && (dataUrl !== undefined || dominant !== undefined);
  const aspect = plan.width && plan.height ? `${plan.width} / ${plan.height}` : undefined;

  const wrapper: CSSProperties = {
    position: "relative",
    display: "inline-block",
    overflow: "hidden",
    maxWidth: "100%",
    verticalAlign: "bottom",
    ...(aspect ? { aspectRatio: aspect } : {}),
    ...(dominant && !loaded ? { backgroundColor: dominant } : {}),
    ...wrapperStyle,
  };

  const img = (
    <img
      {...rest}
      ref={imgRef}
      src={plan.src}
      sizes={sizes}
      alt={alt}
      width={plan.width}
      height={plan.height}
      loading={loading ?? (priority ? "eager" : "lazy")}
      decoding="async"
      {...(priority ? { fetchPriority: "high" as const } : {})}
      onLoad={(e) => {
        setLoaded(true);
        onLoad?.(e);
      }}
      style={{ display: "block", maxWidth: "100%", height: "auto", ...style }}
    />
  );

  return (
    <span className={wrapperClassName} style={wrapper} data-kadr-image="" data-loaded={loaded ? "true" : "false"}>
      {showPlaceholder && dataUrl ? (
        <img
          aria-hidden="true"
          alt=""
          src={dataUrl}
          data-kadr-placeholder=""
          style={{ ...fill, objectFit: "cover", filter: "blur(20px)", transform: "scale(1.1)" }}
        />
      ) : null}
      <picture>
        {plan.sources.map((s) => (
          <source key={s.format} type={s.type} srcSet={s.srcSet} sizes={sizes} />
        ))}
        {img}
      </picture>
    </span>
  );
}
