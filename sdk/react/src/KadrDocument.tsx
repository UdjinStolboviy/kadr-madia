import { useMemo } from "react";
import type { AnchorHTMLAttributes, ReactElement } from "react";
import { KadrImage } from "./KadrImage.js";
import type { KadrAssetLike, KadrRendition, ResolveUrl } from "./types.js";

export interface KadrDocumentProps extends Omit<AnchorHTMLAttributes<HTMLAnchorElement>, "href" | "children"> {
  /** A document asset (`kind: "file"`): a `main` rendition (pdf, docx, …) and optionally a `thumb` (avif/webp). */
  asset: KadrAssetLike;
  /** Shown next to the thumbnail; also the suggested download name. */
  filename?: string;
  /** Sign/resolve private renditions (see `KadrImage`). */
  resolveUrl?: ResolveUrl;
  /** Hide the first-page thumbnail. */
  hideThumbnail?: boolean;
}

const defaultResolve: ResolveUrl = (r) => r.url;

function formatBytes(n?: number): string {
  if (!n || n <= 0) return "";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

/**
 * A download link for a document asset with its first-page thumbnail (PDFs) and
 * a file-type badge. The link downloads the `main` rendition; documents are
 * stored as attachments, so they are never rendered inline from the CDN.
 */
export function KadrDocument(props: KadrDocumentProps): ReactElement | null {
  const { asset, filename, resolveUrl = defaultResolve, hideThumbnail = false, style, ...rest } = props;
  const main: KadrRendition | undefined = useMemo(
    () => asset.renditions.find((r) => r.name === "main") ?? asset.renditions[0],
    [asset],
  );
  const thumbAsset = useMemo<KadrAssetLike>(
    () => ({ renditions: asset.renditions.filter((r) => r.name === "thumb"), placeholder: asset.placeholder }),
    [asset],
  );
  const href = main ? resolveUrl(main) : undefined;
  if (!main || !href) return null;
  const hasThumb = !hideThumbnail && thumbAsset.renditions.length > 0;
  const size = formatBytes(main.bytes);

  return (
    <a
      {...rest}
      href={href}
      download={filename ?? ""}
      rel="noopener"
      style={{ display: "inline-flex", alignItems: "center", gap: 12, textDecoration: "none", color: "inherit", ...style }}
    >
      {hasThumb ? (
        <KadrImage asset={thumbAsset} alt="" resolveUrl={resolveUrl} wrapperStyle={{ width: 64, borderRadius: 4, flex: "none" }} />
      ) : (
        <span
          aria-hidden="true"
          style={{ width: 48, height: 64, display: "inline-flex", alignItems: "center", justifyContent: "center", flex: "none",
            border: "1px solid currentColor", borderRadius: 4, fontSize: 11, fontWeight: 600, opacity: 0.7 }}
        >
          {main.format.toUpperCase()}
        </span>
      )}
      <span style={{ display: "inline-flex", flexDirection: "column", minWidth: 0 }}>
        <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{filename ?? `document.${main.format}`}</span>
        <small style={{ opacity: 0.65 }}>
          {main.format.toUpperCase()}
          {size ? ` · ${size}` : ""}
        </small>
      </span>
    </a>
  );
}
