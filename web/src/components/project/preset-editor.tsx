import { FlaskConical, Loader2 } from "lucide-react";
import { Link } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { toast } from "sonner";

import { Field } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { errorMessage, isApiError } from "@/lib/api";
import { useSavePresetVersion } from "@/lib/queries";
import type { ImageParams, Preset, VideoParams } from "@/lib/types";

type Params = ImageParams | VideoParams;
type Obj = Record<string, unknown>;

function clone<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T;
}

/** Immutable nested set: setIn(obj, ["video","crf"], 24). */
function setIn(root: Obj, path: string[], value: unknown): Obj {
  const next = clone(root);
  let cur = next;
  for (const k of path.slice(0, -1)) {
    const child = cur[k];
    cur[k] = child && typeof child === "object" && !Array.isArray(child) ? child : {};
    cur = cur[k] as Obj;
  }
  cur[path[path.length - 1]!] = value;
  return next;
}

function getIn(root: Obj, path: string[]): unknown {
  let cur: unknown = root;
  for (const k of path) {
    if (!cur || typeof cur !== "object") return undefined;
    cur = (cur as Obj)[k];
  }
  return cur;
}

function NumberInput({
  id,
  value,
  onChange,
  min,
  max,
  step,
  suffix,
}: {
  id: string;
  value: unknown;
  onChange: (v: number | undefined) => void;
  min?: number;
  max?: number;
  step?: number;
  suffix?: string;
}) {
  return (
    <div className="relative">
      <Input
        id={id}
        type="number"
        inputMode="decimal"
        min={min}
        max={max}
        step={step}
        value={typeof value === "number" && Number.isFinite(value) ? value : ""}
        onChange={(e) => onChange(e.target.value === "" ? undefined : Number(e.target.value))}
        className={suffix ? "pr-12" : undefined}
      />
      {suffix && <span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs text-muted-foreground">{suffix}</span>}
    </div>
  );
}

function SwitchRow({ id, label, hint, checked, onChange }: { id: string; label: string; hint?: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <div className="flex items-center justify-between gap-4 rounded-md border px-3 py-2">
      <label htmlFor={id} className="min-w-0 text-sm">
        <span className="font-medium">{label}</span>
        {hint && <span className="block text-xs text-muted-foreground">{hint}</span>}
      </label>
      <Switch id={id} checked={checked} onCheckedChange={onChange} />
    </div>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="grid gap-3">
      <h3 className="border-b pb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">{title}</h3>
      {children}
    </section>
  );
}

function SelectField({ id, value, onChange, options }: { id: string; value: string; onChange: (v: string) => void; options: { value: string; label: string }[] }) {
  return (
    <Select value={value} onValueChange={onChange}>
      <SelectTrigger id={id} className="w-full">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {options.map((o) => (
          <SelectItem key={o.value} value={o.value}>
            {o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

const X264 = ["ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower"];

function ImageForm({ draft, set, err }: { draft: Obj; set: (path: string[], v: unknown) => void; err: (...keys: string[]) => string | undefined }) {
  const q = (getIn(draft, ["quality"]) ?? {}) as Obj;
  const mode = typeof q.mode === "string" ? q.mode : "budget";
  const outputs = Array.isArray(draft.outputs) ? (draft.outputs as Obj[]) : [];
  const has = (f: string) => outputs.some((o) => o.format === f);
  const toggleFormat = (f: string, on: boolean) => {
    const rest = outputs.filter((o) => o.format !== f);
    set(["outputs"], on ? [...outputs, { name: "main", format: f }].sort((a) => (a.format === "avif" ? -1 : 1)) : rest);
  };
  const side = getIn(draft, ["resize", "maxWidth"]);
  const setSide = (v: number | undefined) => {
    set(["resize", "maxWidth"], v);
    set(["resize", "maxHeight"], v);
  };

  return (
    <>
      <Section title="Size">
        <div className="grid grid-cols-2 gap-3">
          <Field label="Max side" htmlFor="pe-side" hint="16–8192 px" error={err("resize")}>
            <NumberInput id="pe-side" value={side} onChange={setSide} min={16} max={8192} step={16} suffix="px" />
          </Field>
          <Field label="Fit" htmlFor="pe-fit">
            <SelectField
              id="pe-fit"
              value={String(getIn(draft, ["resize", "fit"]) ?? "inside")}
              onChange={(v) => set(["resize", "fit"], v)}
              options={[
                { value: "inside", label: "Inside (keep ratio)" },
                { value: "cover", label: "Cover (crop)" },
              ]}
            />
          </Field>
        </div>
        <SwitchRow id="pe-gray" label="Grayscale" checked={getIn(draft, ["color", "grayscale"]) === true} onChange={(v) => set(["color", "grayscale"], v)} />
        <SwitchRow id="pe-norm" label="Normalise" hint="Stretch contrast to the full range" checked={getIn(draft, ["color", "normalise"]) === true} onChange={(v) => set(["color", "normalise"], v)} />
      </Section>

      <Section title="Quality">
        <Field label="Strategy" htmlFor="pe-qmode" error={err("quality.mode")}>
          <SelectField
            id="pe-qmode"
            value={mode}
            onChange={(v) => set(["quality", "mode"], v)}
            options={[
              { value: "budget", label: "Byte budget (fit under a size)" },
              { value: "ssim", label: "Target SSIM (smallest at a quality)" },
              { value: "fixed", label: "Fixed quality" },
            ]}
          />
        </Field>
        {mode === "budget" && (
          <Field label="Max size" htmlFor="pe-maxbytes" hint="1 KB – 10 MB" error={err("quality.maxBytes")}>
            <NumberInput
              id="pe-maxbytes"
              value={typeof q.maxBytes === "number" ? Math.round((q.maxBytes / 1024) * 10) / 10 : undefined}
              onChange={(v) => set(["quality", "maxBytes"], v === undefined ? undefined : Math.round(v * 1024))}
              min={1}
              max={10240}
              suffix="KB"
            />
          </Field>
        )}
        {mode === "ssim" && (
          <Field label="Target SSIM" htmlFor="pe-target" hint="0.90 (smaller) – 0.999 (near-lossless)" error={err("quality.target")}>
            <NumberInput id="pe-target" value={q.target} onChange={(v) => set(["quality", "target"], v)} min={0.9} max={0.999} step={0.001} />
          </Field>
        )}
        {mode === "fixed" ? (
          <Field label="Quality (q)" htmlFor="pe-q" hint="1–100" error={err("quality.q")}>
            <NumberInput id="pe-q" value={q.q} onChange={(v) => set(["quality", "q"], v)} min={1} max={100} />
          </Field>
        ) : (
          <div className="grid grid-cols-2 gap-3">
            <Field label="q min" htmlFor="pe-qmin" hint="Lower bound searched">
              <NumberInput id="pe-qmin" value={q.qMin} onChange={(v) => set(["quality", "qMin"], v)} min={1} max={100} />
            </Field>
            <Field label="q max" htmlFor="pe-qmax" hint="Upper bound searched">
              <NumberInput id="pe-qmax" value={q.qMax} onChange={(v) => set(["quality", "qMax"], v)} min={1} max={100} />
            </Field>
          </div>
        )}
      </Section>

      <Section title="Outputs">
        <SwitchRow id="pe-avif" label="AVIF" hint="Smallest, modern browsers" checked={has("avif")} onChange={(v) => toggleFormat("avif", v)} />
        <SwitchRow id="pe-webp" label="WebP" hint="Wide compatibility fallback" checked={has("webp")} onChange={(v) => toggleFormat("webp", v)} />
        {err("outputs") && <p className="text-xs text-destructive">{err("outputs")}</p>}
        <SwitchRow id="pe-th" label="ThumbHash" hint="Tiny blurred placeholder" checked={getIn(draft, ["placeholder", "thumbhash"]) === true} onChange={(v) => set(["placeholder", "thumbhash"], v)} />
        <SwitchRow id="pe-dc" label="Dominant colour" checked={getIn(draft, ["placeholder", "dominantColor"]) === true} onChange={(v) => set(["placeholder", "dominantColor"], v)} />
        <SwitchRow id="pe-retain" label="Retain original" hint="Keep the uploaded file as well" checked={draft.retainOriginal === true} onChange={(v) => set(["retainOriginal"], v)} />
      </Section>
    </>
  );
}

function VideoForm({ draft, set, err }: { draft: Obj; set: (path: string[], v: unknown) => void; err: (...keys: string[]) => string | undefined }) {
  const str = (path: string[], fallback: string) => {
    const v = getIn(draft, path);
    return typeof v === "string" && v ? v : fallback;
  };
  const text = (id: string, path: string[], placeholder: string) => (
    <Input id={id} value={str(path, "")} placeholder={placeholder} onChange={(e) => set(path, e.target.value.trim())} className="font-mono" />
  );
  return (
    <>
      <Section title="Video">
        <div className="grid grid-cols-2 items-start gap-3">
          <Field label="Max width" htmlFor="pe-vw" hint="160–4096 px" error={err("video.maxWidth")}>
            <NumberInput id="pe-vw" value={getIn(draft, ["video", "maxWidth"])} onChange={(v) => set(["video", "maxWidth"], v)} min={160} max={4096} step={2} suffix="px" />
          </Field>
          <Field label="Frame rate" htmlFor="pe-fps" hint="1–60" error={err("video.fps")}>
            <NumberInput id="pe-fps" value={getIn(draft, ["video", "fps"])} onChange={(v) => set(["video", "fps"], v)} min={1} max={60} suffix="fps" />
          </Field>
          <Field label="CRF" htmlFor="pe-crf" hint="14 (best) – 40 (smallest)" error={err("video.crf")}>
            <NumberInput id="pe-crf" value={getIn(draft, ["video", "crf"])} onChange={(v) => set(["video", "crf"], v)} min={14} max={40} />
          </Field>
          <Field label="x264 preset" htmlFor="pe-x264" hint="Slower = smaller">
            <SelectField id="pe-x264" value={str(["video", "preset"], "slow")} onChange={(v) => set(["video", "preset"], v)} options={X264.map((v) => ({ value: v, label: v }))} />
          </Field>
          <Field label="Max bitrate" htmlFor="pe-maxrate" hint="e.g. 900k or 4M" error={err("video.maxrate")}>
            {text("pe-maxrate", ["video", "maxrate"], "1200k")}
          </Field>
          <Field label="Buffer size" htmlFor="pe-bufsize" hint="Usually 2× max bitrate" error={err("video.bufsize")}>
            {text("pe-bufsize", ["video", "bufsize"], "2400k")}
          </Field>
        </div>
        <Field label="Hardware encoding" htmlFor="pe-hw" error={err("video.hw")}>
          <SelectField
            id="pe-hw"
            value={str(["video", "hw"], "auto")}
            onChange={(v) => set(["video", "hw"], v)}
            options={[
              { value: "auto", label: "Auto (VAAPI when available)" },
              { value: "vaapi", label: "VAAPI" },
              { value: "off", label: "Off (software x264)" },
            ]}
          />
        </Field>
      </Section>

      <Section title="Audio">
        <div className="grid grid-cols-3 gap-3">
          <Field label="Bitrate" htmlFor="pe-abr" error={err("audio.bitrate")}>
            {text("pe-abr", ["audio", "bitrate"], "96k")}
          </Field>
          <Field label="Channels" htmlFor="pe-ach" error={err("audio.channels")}>
            <SelectField
              id="pe-ach"
              value={String(getIn(draft, ["audio", "channels"]) ?? 2)}
              onChange={(v) => set(["audio", "channels"], Number(v))}
              options={[
                { value: "1", label: "Mono" },
                { value: "2", label: "Stereo" },
              ]}
            />
          </Field>
          <Field label="Sample rate" htmlFor="pe-asr">
            <SelectField
              id="pe-asr"
              value={String(getIn(draft, ["audio", "sampleRate"]) ?? 44100)}
              onChange={(v) => set(["audio", "sampleRate"], Number(v))}
              options={[
                { value: "44100", label: "44.1 kHz" },
                { value: "48000", label: "48 kHz" },
              ]}
            />
          </Field>
        </div>
      </Section>

      <Section title="Output">
        <Field label="Poster max size" htmlFor="pe-poster" hint="Poster frame size budget">
          <NumberInput
            id="pe-poster"
            value={typeof getIn(draft, ["poster", "maxBytes"]) === "number" ? Math.round(((getIn(draft, ["poster", "maxBytes"]) as number) / 1024) * 10) / 10 : undefined}
            onChange={(v) => set(["poster", "maxBytes"], v === undefined ? undefined : Math.round(v * 1024))}
            min={1}
            max={1024}
            suffix="KB"
          />
        </Field>
        <SwitchRow id="pe-hls" label="HLS ladder" hint="Also produce an adaptive HLS package" checked={draft.hls === true} onChange={(v) => set(["hls"], v)} />
        <SwitchRow id="pe-vretain" label="Retain original" hint="Keep the uploaded file as well" checked={draft.retainOriginal === true} onChange={(v) => set(["retainOriginal"], v)} />
      </Section>
    </>
  );
}

export function PresetEditor({
  projectId,
  preset,
  open,
  onOpenChange,
}: {
  projectId: string;
  preset: Preset | null;
  open: boolean;
  onOpenChange: (o: boolean) => void;
}) {
  const save = useSavePresetVersion(projectId);
  const [draft, setDraft] = useState<Obj>({});
  const [loaded, setLoaded] = useState<string | null>(null);

  // Re-seed the draft whenever a different preset version is opened.
  const key = preset ? `${preset.id}:${preset.current_version}:${open}` : null;
  if (preset && open && key !== loaded) {
    setLoaded(key);
    setDraft(clone(preset.params as Obj));
    save.reset();
  }

  const set = (path: string[], v: unknown) => setDraft((d) => setIn(d, path, v));
  const serverMsg = isApiError(save.error) ? (save.error.fieldErrors.params ?? save.error.fieldErrors.slug) : undefined;
  const err = (...keys: string[]) => (serverMsg && keys.some((k) => serverMsg.startsWith(k)) ? serverMsg : undefined);
  const isVideo = preset?.kind === "video";

  const onSave = () => {
    if (!preset) return;
    save.mutate(
      { slug: preset.slug, params: draft as unknown as Params },
      {
        onSuccess: (p) => {
          toast.success(`Saved ${p.slug} v${p.current_version}`);
          onOpenChange(false);
        },
        onError: (e) =>
          toast.error("Couldn't save preset", {
            description: isApiError(e) ? (e.fieldErrors.params ?? e.fieldErrors.slug ?? errorMessage(e)) : errorMessage(e),
          }),
      },
    );
  };

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 sm:max-w-lg">
        <SheetHeader className="border-b">
          <SheetTitle>
            Edit <span className="font-mono">{preset?.slug}</span>
          </SheetTitle>
          <SheetDescription>
            Saving adds a new version. Assets already processed keep the version they were created with.
          </SheetDescription>
        </SheetHeader>
        <div className="grid flex-1 content-start gap-6 overflow-y-auto p-4">
          {preset && (isVideo ? <VideoForm draft={draft} set={set} err={err} /> : <ImageForm draft={draft} set={set} err={err} />)}
          {serverMsg && (
            <p className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive" role="alert">
              {serverMsg}
            </p>
          )}
          {save.isError && !serverMsg && (
            <p className="text-sm text-destructive" role="alert">
              {errorMessage(save.error)}
            </p>
          )}
        </div>
        <SheetFooter className="flex-row items-center justify-between border-t">
          {!isVideo && preset ? (
            <Button variant="ghost" size="sm" asChild>
              <Link to="/playground" search={{ project: projectId, preset: preset.slug }}>
                <FlaskConical /> Try it in Playground
              </Link>
            </Button>
          ) : (
            <span />
          )}
          <div className="flex gap-2">
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button onClick={onSave} disabled={save.isPending}>
              {save.isPending && <Loader2 className="animate-spin" />}
              Save version
            </Button>
          </div>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}
