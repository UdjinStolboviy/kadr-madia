# Пайплайни обробки

## 1. Пресети

Пресет — іменований набір параметрів обробки на рівні проєкту. Кожна зміна створює нову **незмінну версію** (`preset_versions`). Asset памʼятає версію, з якою його обробили, тож результат завжди можна відтворити, а старі URL не ламаються.

### Стандартні пресети (створюються для кожного нового проєкту)

| slug | Тип | Призначення | Ключові параметри |
|---|---|---|---|
| `photo-lite` | image | Картки, інвентар, мініатюри | max side 800, **≤ 20 480 B**, AVIF+WebP, sharpen light |
| `photo-balanced` | image | Перегляд на весь екран | max side 1440, ціль SSIM, AVIF q≈52 + WebP |
| `doc-lite` | image | Чеки, договори, етикетки | grayscale + normalise, max side 1100, **≤ 20 480 B**, sharpen 0.85 |
| `avatar` | image | Аватари | smart crop 1:1 (attention), 256 px, ≤ 8 КБ |
| `video-720p` | video | Звичайні кліпи | H.264 CRF 26, maxrate 900k, 30 fps, AAC 64k mono, постер ≤ 15 КБ |
| `video-480p-eco` | video | Економ | H.264 CRF 28, maxrate 500k, 854 px |

### Схема параметрів (`params_json`)

```jsonc
// image
{
  "kind": "image",
  "resize": { "maxWidth": 800, "maxHeight": 800, "fit": "inside", "crop": null /* "attention" | "centre" */, "aspect": null },
  "color": { "grayscale": false, "normalise": false },
  "sharpen": { "sigma": 0.6, "m1": 0.5, "m2": 1.5 },   // null = вимкнено
  "outputs": [
    { "name": "main", "format": "avif", "effort": 4, "chroma": "4:2:0" },
    { "name": "main", "format": "webp", "smartSubsample": true }
  ],
  "quality": { "mode": "budget", "maxBytes": 20480, "qMax": 60, "qMin": 30, "downscaleStep": 0.85, "minSide": 320 },
  // або { "mode": "fixed", "q": 52 } | { "mode": "ssim", "target": 0.985 }
  "placeholder": { "thumbhash": true, "dominantColor": true },
  "limits": { "maxInputBytes": 26214400, "maxInputPixels": 40000000, "accept": ["jpeg","png","webp","heic","avif","gif"] },
  "retainOriginal": false
}

// video
{
  "kind": "video",
  "video": { "codec": "h264", "maxWidth": 1280, "fps": 30, "crf": 26, "maxrate": "900k", "bufsize": "1800k", "preset": "slow", "hw": "auto" },
  "audio": { "codec": "aac", "bitrate": "64k", "channels": 1, "sampleRate": 44100 },
  "container": { "format": "mp4", "faststart": true },
  "poster": { "atSec": 1.0, "preset": "photo-lite", "maxBytes": 15360 },
  "limits": { "maxInputBytes": 2147483648, "maxDurationSec": 300, "maxWidth": 4096, "maxHeight": 4096 },
  "retainOriginal": false
}
```

## 2. Фото

```mermaid
flowchart LR
  A[сирець у tmp] --> B{magic bytes<br/>+ розмір + пікселі}
  B -- ні --> X[failed: unsupported_media]
  B -- так --> C[vips: autorotate,<br/>sRGB, strip metadata]
  C --> D[resize lanczos3<br/>/ smart crop]
  D --> E[grayscale + normalise?]
  E --> F[sharpen]
  F --> G[проміжний .v файл]
  G --> H[бюджетне кодування<br/>AVIF і WebP]
  G --> I[100px → ThumbHash<br/>+ dominant color]
  H --> J[PUT рендицій у R2]
  I --> K[метадані Blob]
  J --> K --> L[asset.ready]
```

**Чому проміжний `.v`:** декодування й ресайз (найдорожче) виконуються один раз; кодування з різною якістю працює з уже готового маленького растра й займає кілька десятків мс.

### Алгоритм бюджетного кодування

```
side := min(maxSide, original long side)
loop:
  lo, hi := qMin, qMax
  best := nil
  while lo <= hi:                      # бінарний пошук, ≤ 5 ітерацій
    q := (lo + hi) / 2
    out := encode(raster(side), q)
    if len(out) <= maxBytes: best = out; lo = q + 1
    else: hi = q - 1
  if best != nil: return best          # найвища якість, що вміщується в бюджет
  if side*downscaleStep < minSide: return encode(raster(minSide), qMin)  # гарантія результату, прапорець over_budget
  side = side * downscaleStep          # зменшити пікселі, а не душити якість
```

Принцип: **краще менше пікселів із нормальною якістю, ніж багато пікселів у «милі».**

### Чому libvips CLI, а не imgproxy

Див. [ADR-0007](adr/0007-libvips-cli-image-engine.md). Коротко: 0 МБ RAM у простої, повний набір операцій (grayscale, normalise, smart crop), немає питань із ліцензією Pro-функцій, не потрібен CGO. imgproxy лишається варіантом для майбутніх рендицій на вимогу.

## 3. Відео

```mermaid
flowchart LR
  A[сирець у tmp] --> B[ffprobe -show_streams -show_format]
  B --> C{тривалість, роздільність,<br/>кодеки, розмір ≤ ліміту?}
  C -- ні --> X[failed]
  C -- так --> D{є /dev/dri і hw=auto?}
  D -- так --> E[h264_vaapi]
  D -- ні --> F[libx264 -preset slow -threads 2]
  E & F --> G[MP4 +faststart<br/>-map_metadata -1]
  G --> H[постер: кадр 1с → photo-lite ≤ 15 КБ]
  H --> I[ThumbHash постера]
  G & H --> J[PUT у R2] --> K[asset.ready]
```

### Команда (програмний режим, libx264)

```bash
nice -n 10 ionice -c3 ffmpeg -hide_banner -nostdin -y -i in \
  -map 0:v:0 -map 0:a:0? -map_metadata -1 -sn -dn \
  -vf "scale='min(1280,iw)':-2:flags=lanczos,fps=30" \
  -c:v libx264 -profile:v high -level 4.1 -pix_fmt yuv420p \
  -crf 26 -preset slow -maxrate 900k -bufsize 1800k \
  -c:a aac -b:a 64k -ac 1 -ar 44100 \
  -movflags +faststart -threads 2 \
  -progress pipe:1 out.mp4
```

### Команда (VAAPI, Intel iGPU)

```bash
ffmpeg -hide_banner -nostdin -y -vaapi_device /dev/dri/renderD128 -i in \
  -map 0:v:0 -map 0:a:0? -map_metadata -1 \
  -vf "fps=30,format=nv12,hwupload,scale_vaapi=w='min(1280,iw)':h=-2" \
  -c:v h264_vaapi -rc_mode QVBR -global_quality 26 -maxrate 900k -bufsize 1800k \
  -c:a aac -b:a 64k -ac 1 -ar 44100 -movflags +faststart -progress pipe:1 out.mp4
```

Під час старту Kadr перевіряє, які енкодери доступні (`ffmpeg -encoders`, пробне кодування 1 кадру через VAAPI), і показує це в адмінці на сторінці System → Capabilities.

### Прогрес

`-progress pipe:1` → парсинг `out_time_us` → `jobs.progress` (0–100) → адмінка (SSE). Для проєктів — лише фінальні події (`asset.ready` / `asset.failed`).

### Далі (M5)

- HLS-драбина 360/480/720/1080 для відео > 60 с, сегменти fMP4.
- Спрайти таймлайну (VTT) і 3-секундне беззвучне превʼю.
- AV1 (SVT-AV1) як додаткова рендиція для довгого контенту.

## 4. Гарантії якості

- **Еталонний набір тестів** (`test/fixtures`): фото, скріншоти, документи, нічні кадри, HEIC з iPhone, відео з поворотом і VFR.
- **Golden-тести в CI:** для кожного пресету перевіряємо `bytes ≤ maxBytes`, мінімальний SSIM відносно зменшеного еталона і стабільність розмірів.
- **Метрика економії:** `original_bytes` vs `sum(rendition bytes)` для кожного Blob → дашборд «прийнято / збережено / зекономлено».
