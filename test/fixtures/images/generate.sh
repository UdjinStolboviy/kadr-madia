#!/usr/bin/env bash
# Regenerates the synthetic image fixtures used by the image engine golden
# tests (internal/engine/image). Everything is generated locally with
# libvips and ffmpeg; nothing is downloaded, so the files are license-free.
#
# Requirements: vips (with libheif + HEVC encoder for sample.heic), ffmpeg,
# exiftool (optional; falls back to libvips for the EXIF orientation).
#
# Usage: test/fixtures/images/generate.sh
set -euo pipefail

cd "$(dirname "$0")"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

ff() { ffmpeg -hide_banner -loglevel error -nostdin -y "$@"; }

# 1. photo.jpg: photo-like 1600x1200, smooth four-colour gradient blended
#    with a soft fractal for structure, plus grain.
ff -f lavfi -i "gradients=s=1600x1200:c0=0x3a6ea5:c1=0xf2c14e:c2=0x2d6a4f:c3=0xe76f51:nb_colors=4:x0=0:y0=0:x1=1600:y1=1200:seed=7:d=1" \
   -f lavfi -i "mandelbrot=s=1600x1200:maxiter=200:start_scale=2.5:end_scale=2.5" \
   -filter_complex "[1:v]gblur=sigma=2[m];[0:v][m]blend=all_mode=overlay:all_opacity=0.45,noise=alls=9:allf=t,format=yuvj420p" \
   -frames:v 1 -q:v 3 photo.jpg

# 2. texture.jpg: high-detail texture (fractal edge detail + strong noise),
#    the worst case for a byte budget.
ff -f lavfi -i "mandelbrot=s=1000x1000:maxiter=600:start_x=-0.743643887037151:start_y=0.13182590420533:start_scale=0.002:end_scale=0.002" \
   -vf "noise=alls=20:allf=t,format=yuvj420p" -frames:v 1 -q:v 5 texture.jpg

# 3. screenshot.png: flat UI-like image with large uniform areas and text.
ff -f lavfi -i "color=c=0xf5f6f8:s=1280x800:d=1" \
   -vf "drawbox=x=0:y=0:w=1280:h=56:color=0x1f2937:t=fill,\
drawbox=x=0:y=56:w=240:h=744:color=0xe5e7eb:t=fill,\
drawbox=x=24:y=96:w=192:h=28:color=0x93c5fd:t=fill,\
drawbox=x=24:y=140:w=160:h=20:color=0xcbd5e1:t=fill,\
drawbox=x=24:y=176:w=170:h=20:color=0xcbd5e1:t=fill,\
drawbox=x=280:y=96:w=440:h=220:color=0xffffff:t=fill,\
drawbox=x=280:y=96:w=440:h=220:color=0xd1d5db:t=2,\
drawbox=x=760:y=96:w=480:h=220:color=0xffffff:t=fill,\
drawbox=x=760:y=96:w=480:h=220:color=0xd1d5db:t=2,\
drawbox=x=300:y=230:w=60:h=70:color=0x2563eb:t=fill,\
drawbox=x=380:y=190:w=60:h=110:color=0x2563eb:t=fill,\
drawbox=x=460:y=150:w=60:h=150:color=0x2563eb:t=fill,\
drawbox=x=540:y=210:w=60:h=90:color=0x2563eb:t=fill,\
drawbox=x=280:y=356:w=960:h=400:color=0xffffff:t=fill,\
drawbox=x=280:y=356:w=960:h=400:color=0xd1d5db:t=2,\
drawbox=x=1080:y=12:w=160:h=32:color=0x10b981:t=fill" \
   -frames:v 1 "$tmp/ui.png"
vips text "$tmp/ui-text.v" "<b>acme</b> dashboard     Projects   Usage   Settings" --dpi 110 --font "sans"
vips text "$tmp/table.v" "$(printf 'Name                Status      Size        Updated\nreceipt-001.jpg     ready       18.2 KB     2 min ago\nlisting-77.heic     ready       19.9 KB     5 min ago\navatar-k3.png       processing  -           just now\nscan-0042.pdf       failed      -           1 h ago\nclip-intro.mp4      ready       2.1 MB      yesterday')" --dpi 100 --font "monospace"
vips linear --uchar -- "$tmp/ui-text.v" "$tmp/ui-text-inv.v" -1 255
vips linear --uchar -- "$tmp/table.v" "$tmp/table-inv.v" -0.85 255
vips colourspace "$tmp/ui-text-inv.v" "$tmp/ui-text-c.v" srgb
vips colourspace "$tmp/table-inv.v" "$tmp/table-c.v" srgb
vips insert "$tmp/ui.png" "$tmp/table-c.v" "$tmp/ui1.v" 300 380
vips insert "$tmp/ui1.v" "$tmp/ui-text-c.v" "$tmp/ui2.v" 760 120
vips copy "$tmp/ui2.v" screenshot.png[compression=9]

# 4. document.jpg: scanned-document look; dark text on slightly gray paper so
#    normalise has work to do.
vips text "$tmp/doc.v" "$(cat <<'TXT'
<b>INVOICE  No. 2026-0415</b>

Seller: Acme Supplies Ltd., 1 Example Street, Example City
Buyer:  Example Corp., 42 Sample Avenue, Sampletown

Item                                   Qty      Price      Total
Paper, A4, 500 sheets                    4      5.20       20.80
Toner cartridge, black                   1     64.00       64.00
Shipping and handling                    1      7.50        7.50

Subtotal                                                   92.30
VAT 20%                                                    18.46
<b>Total due                                                 110.76</b>

Payment terms: 14 days from the invoice date. Please quote the
invoice number with your payment. Thank you for your business.
TXT
)" --width 1500 --dpi 220 --font "monospace"
vips embed "$tmp/doc.v" "$tmp/doc-pad.v" 90 120 1680 2200
vips linear --uchar -- "$tmp/doc-pad.v" "$tmp/doc-paper.v" -0.78 222
vips gaussnoise "$tmp/noise.v" 1680 2200 --sigma 4 --mean 0
vips add "$tmp/doc-paper.v" "$tmp/noise.v" "$tmp/doc-noisy.v"
vips cast "$tmp/doc-noisy.v" "$tmp/doc-final.v" uchar
vips jpegsave "$tmp/doc-final.v" document.jpg --Q 85

# 5. alpha.png: RGBA with a soft-edged transparent background.
ff -f lavfi -i "color=c=black:s=800x800:d=1" \
   -vf "format=rgba,geq=r='X*255/W':g='Y*255/H':b='128+100*sin(X/23)':a='255*clip((330-hypot(X-400,Y-400))/12,0,1)'" \
   -frames:v 1 alpha.png

# 6. rotated.jpg: stored landscape 1200x800 with a red block in the stored
#    top-left corner and EXIF Orientation=6 (display rotated 90° CW). After
#    autorotation the image is portrait 800x1200 and the red block is in the
#    top-right corner.
ff -f lavfi -i "gradients=s=1200x800:c0=0x4b5563:c1=0x9ca3af:x0=0:y0=0:x1=1200:y1=800:d=1" \
   -vf "drawbox=x=0:y=0:w=240:h=200:color=0xff0000:t=fill,drawbox=x=1000:y=650:w=200:h=150:color=0x0000ff:t=fill" \
   -frames:v 1 -q:v 3 "$tmp/rot.jpg"
if command -v exiftool >/dev/null 2>&1; then
  exiftool -q -overwrite_original -n -Orientation=6 -o rotated.jpg "$tmp/rot.jpg"
else
  vips copy "$tmp/rot.jpg" "$tmp/rot.v"
  vips copy "$tmp/rot.v" rotated.jpg[Q=90] --set-type int --set-name orientation --set-value 6 2>/dev/null \
    || { echo "exiftool missing and libvips cannot set orientation; rotated.jpg not generated" >&2; }
fi

# 7. sample.heic: HEVC-coded HEIF from the photo (skipped when libheif has
#    no HEVC encoder).
if vips heifsave photo.jpg "$tmp/sample.heic" --compression hevc --Q 50 2>/dev/null; then
  vips thumbnail photo.jpg "$tmp/photo-small.v" 1024
  vips heifsave "$tmp/photo-small.v" sample.heic --compression hevc --Q 50
else
  echo "note: libheif has no HEVC encoder; sample.heic not generated" >&2
fi

# 8. animated.gif: 6-frame animation; the engine must use the first page.
ff -f lavfi -i "testsrc2=s=320x240:r=6:d=1" -vf "split[a][b];[a]palettegen[p];[b][p]paletteuse" -loop 0 animated.gif

# 9. corrupt.jpg: photo.jpg truncated to 40% of its size.
size=$(wc -c <photo.jpg)
head -c $((size * 40 / 100)) photo.jpg >corrupt.jpg

# 10. not-an-image.txt: plain text for the unsupported-media test.
printf 'This is not an image.\n' >not-an-image.txt

ls -l
du -ch -- * | tail -1
