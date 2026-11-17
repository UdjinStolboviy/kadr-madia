import { cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { KadrDocument, KadrImage, KadrVideo } from "../src/index.js";
import type { KadrAssetLike } from "../src/index.js";

afterEach(cleanup);

const image: KadrAssetLike = {
  renditions: [
    { name: "main", format: "avif", width: 800, height: 600, url: "https://cdn/x/main.avif" },
    { name: "main", format: "webp", width: 800, height: 600, url: "https://cdn/x/main.webp" },
    { name: "small", format: "webp", width: 400, height: 300, url: "https://cdn/x/small.webp" },
  ],
  placeholder: { thumbhash: "YxUKNJpwd3dxiHh3iHdwcQjnhw==", dominant_color: "#a3866c" },
  original: { width: 4032, height: 3024 },
};

describe("KadrImage", () => {
  it("renders a picture with avif and webp sources, dimensions and lazy loading", () => {
    const { container } = render(<KadrImage asset={image} alt="cat" sizes="400px" />);
    const sources = container.querySelectorAll("picture source");
    expect([...sources].map((s) => s.getAttribute("type"))).toEqual(["image/avif", "image/webp"]);
    expect(sources[0]!.getAttribute("srcset")).toBe("https://cdn/x/main.avif 800w");
    expect(sources[1]!.getAttribute("srcset")).toBe("https://cdn/x/small.webp 400w, https://cdn/x/main.webp 800w");
    const img = container.querySelector("picture img")!;
    expect(img.getAttribute("src")).toBe("https://cdn/x/main.webp");
    expect(img.getAttribute("width")).toBe("800");
    expect(img.getAttribute("height")).toBe("600");
    expect(img.getAttribute("loading")).toBe("lazy");
    expect(img.getAttribute("alt")).toBe("cat");
  });

  it("shows a blurred thumbhash placeholder until the image loads", () => {
    const { container } = render(<KadrImage asset={image} alt="cat" />);
    const ph = container.querySelector("[data-kadr-placeholder]")!;
    expect(ph.getAttribute("src")).toMatch(/^data:image\/png;base64,/);
    expect((ph as HTMLElement).style.filter).toContain("blur");
    fireEvent.load(container.querySelector("picture img")!);
    expect(container.querySelector("[data-kadr-placeholder]")).toBeNull();
  });

  it("supports priority, disabled placeholder and private renditions via resolveUrl", () => {
    const priv: KadrAssetLike = {
      renditions: [{ name: "main", format: "webp", width: 10, height: 10, key: "acme/blb/main.webp" }],
    };
    const { container } = render(
      <KadrImage asset={priv} alt="" priority placeholder={false} resolveUrl={(r) => `https://signed/${r.key}`} />,
    );
    const img = container.querySelector("img")!;
    expect(img.getAttribute("src")).toBe("https://signed/acme/blb/main.webp");
    expect(img.getAttribute("loading")).toBe("eager");
    expect(img.getAttribute("fetchpriority")).toBe("high");
    expect(container.querySelector("[data-kadr-placeholder]")).toBeNull();
  });
});

const video: KadrAssetLike = {
  renditions: [{ name: "main", format: "mp4", width: 1280, height: 720, url: "https://cdn/v/main.mp4" }],
  poster: { name: "poster", format: "webp", width: 1280, height: 720, url: "https://cdn/v/poster.webp" },
  placeholder: { thumbhash: "YxUKNJpwd3dxiHh3iHdwcQjnhw==" },
};

describe("KadrVideo", () => {
  it("renders mp4 sources and the poster rendition", () => {
    const { container } = render(<KadrVideo asset={video} controls />);
    const v = container.querySelector("video")!;
    expect(v.getAttribute("poster")).toBe("https://cdn/v/poster.webp");
    expect(v.getAttribute("width")).toBe("1280");
    const s = v.querySelector("source")!;
    expect(s.getAttribute("src")).toBe("https://cdn/v/main.mp4");
    expect(s.getAttribute("type")).toBe("video/mp4");
  });

  it("falls back to a thumbhash poster", () => {
    const { container } = render(<KadrVideo asset={{ ...video, poster: null }} />);
    expect(container.querySelector("video")!.getAttribute("poster")).toMatch(/^data:image\/png;base64,/);
  });
});

describe("KadrDocument", () => {
  const doc: KadrAssetLike = {
    renditions: [
      { name: "main", format: "pdf", bytes: 204800, url: "https://cdn/x/main.pdf" },
      { name: "thumb", format: "avif", width: 480, height: 640, url: "https://cdn/x/thumb.avif" },
      { name: "thumb", format: "webp", width: 480, height: 640, url: "https://cdn/x/thumb.webp" },
    ],
  };

  it("links to the main file and shows the first-page thumbnail, not the document as an image", () => {
    const { container } = render(<KadrDocument asset={doc} filename="contract.pdf" />);
    const a = container.querySelector("a")!;
    expect(a.getAttribute("href")).toBe("https://cdn/x/main.pdf");
    expect(a.getAttribute("download")).toBe("contract.pdf");
    expect([...container.querySelectorAll("picture source")].map((s) => s.getAttribute("type"))).toEqual(["image/avif", "image/webp"]);
    expect(container.textContent).toContain("contract.pdf");
    expect(container.textContent).toContain("PDF · 200 KB");
  });

  it("falls back to a type badge without a thumbnail and resolves private keys", () => {
    const priv: KadrAssetLike = { renditions: [{ name: "main", format: "docx", key: "acme/blb_1/main.docx" }] };
    const { container } = render(<KadrDocument asset={priv} resolveUrl={(r) => `https://media/${r.key}?sig=1`} />);
    expect(container.querySelector("a")!.getAttribute("href")).toBe("https://media/acme/blb_1/main.docx?sig=1");
    expect(container.querySelector("picture")).toBeNull();
    expect(container.textContent).toContain("DOCX");
    expect(render(<KadrDocument asset={{ renditions: [{ name: "main", format: "pdf", key: "k" }] }} />).container.innerHTML).toBe("");
  });
});
