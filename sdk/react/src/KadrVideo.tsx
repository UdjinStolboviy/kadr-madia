import { useMemo } from "react";
import type { ReactElement, VideoHTMLAttributes } from "react";
import { planVideo } from "./renditions.js";
import { safeThumbHashToDataURL } from "./thumbhash.js";
import type { KadrAssetLike, ResolveUrl } from "./types.js";

export interface KadrVideoProps
  extends Omit<VideoHTMLAttributes<HTMLVideoElement>, "src" | "poster" | "width" | "height" | "children"> {
  asset: KadrAssetLike;
  /** Sign/resolve private renditions. */
  resolveUrl?: ResolveUrl;
}

/**
 * `<video>` with the asset's mp4 renditions and its poster. When the asset has
 * no poster rendition, the ThumbHash placeholder is decoded into a data URL
 * and used as the poster instead.
 */
export function KadrVideo(props: KadrVideoProps): ReactElement {
  const { asset, resolveUrl, preload = "metadata", style, ...rest } = props;
  const plan = useMemo(() => planVideo(asset, resolveUrl), [asset, resolveUrl]);
  const hash = asset.placeholder?.thumbhash;
  const fallbackPoster = useMemo(() => (plan.poster ? undefined : safeThumbHashToDataURL(hash)), [plan.poster, hash]);
  const poster = plan.poster ?? fallbackPoster;
  const aspect = plan.width && plan.height ? `${plan.width} / ${plan.height}` : undefined;
  return (
    <video
      {...rest}
      poster={poster}
      width={plan.width}
      height={plan.height}
      preload={preload}
      playsInline={rest.playsInline ?? true}
      style={{ maxWidth: "100%", height: "auto", ...(aspect ? { aspectRatio: aspect } : {}), ...style }}
    >
      {plan.sources.map((s) => (
        <source key={s.src} src={s.src} type={s.type} />
      ))}
    </video>
  );
}
