import { useMutation, useQuery } from "@tanstack/react-query";
import { getRouteApi } from "@tanstack/react-router";
import { AlertTriangle, FlaskConical, FolderKanban, Loader2, Play, Save, Upload } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";

import { Field, PageHeader, StatusBadge } from "@/components/common";
import { ProjectSelect } from "@/components/project-select";
import { EmptyState, ErrorState } from "@/components/states";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api, errorMessage, isApiError } from "@/lib/api";
import { formatBytes, formatMs } from "@/lib/format";
import { presetsQuery, useSavePresetVersion } from "@/lib/queries";
import { useActiveProject } from "@/lib/use-active-project";
import { SLUG_RE, type ImageParams, type PlaygroundRendition, type PlaygroundResult, type Preset } from "@/lib/types";

const route = getRouteApi("/app/playground");
const MAX_SAMPLE = 25 * 1024 * 1024;
const FORMATS = ["avif", "webp"] as const;

const FALLBACK: ImageParams = {
  kind: "image",
  resize: { maxWidth: 1200, maxHeight: 1200, fit: "inside", crop: null, aspect: null },
  color: { grayscale: false, normalise: false },
  sharpen: null,
  outputs: [
    { name: "main", format: "avif" },
    { name: "main", format: "webp" },
  ],
  quality: { mode: "budget", maxBytes: 51200, qMax: 60, qMin: 30 },
  placeholder: { thumbhash: true, dominantColor: true },
};

/** Fill in the pieces the form edits so partial server params never produce undefined reads. */
function normalize(p: Record<string, unknown> | undefined): ImageParams {
  const base = structuredClone((p ?? FALLBACK) as unknown) as Partial<ImageParams>;
  return {
    ...FALLBACK,
    ...base,
    resize: { ...FALLBACK.resize, ...base.resize },
    color: { ...FALLBACK.color, ...base.color },
    quality: { ...FALLBACK.quality, ...base.quality },
    outputs: base.outputs?.length ? base.outputs : FALLBACK.outputs,
  } as ImageParams;
}

function NumInput({
  id,
  value,
  onChange,
  min,
  max,
  step,
}: {
  id: string;
  value: number | undefined;
  onChange: (v: number | undefined) => void;
  min?: number;
  max?: number;
  step?: number;
}) {
  return (
    <Input
      id={id}
      type="number"
      inputMode="decimal"
      min={min}
      max={max}
      step={step}
      value={value ?? ""}
      onChange={(e) => onChange(e.target.value === "" ? undefined : Number(e.target.value))}
    />
  );
}

function Toggle({ id, label, checked, onChange }: { id: string; label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label htmlFor={id} className="flex items-center gap-2 text-sm">
      <Checkbox id={id} checked={checked} onCheckedChange={(c) => onChange(c === true)} />
      {label}
    </label>
  );
}

function ParamsForm({ params, onChange }: { params: ImageParams; onChange: (p: ImageParams) => void }) {
  const q = params.quality;
  const setQ = (patch: Partial<ImageParams["quality"]>) => onChange({ ...params, quality: { ...q, ...patch } });
  const setResize = (patch: Partial<ImageParams["resize"]>) => onChange({ ...params, resize: { ...params.resize, ...patch } });
  const hasFormat = (f: string) => params.outputs.some((o) => o.format === f);
  const toggleFormat = (f: string, on: boolean) => {
    if (on && !hasFormat(f)) onChange({ ...params, outputs: [...params.outputs, { name: "main", format: f }] });
    if (!on) onChange({ ...params, outputs: params.outputs.filter((o) => o.format !== f) });
  };

  return (
    <div className="grid gap-4">
      <div className="grid grid-cols-2 gap-3">
        <Field label="Max width" htmlFor="pg-w">
          <NumInput id="pg-w" value={params.resize.maxWidth} min={16} max={8192} onChange={(v) => setResize({ maxWidth: v ?? 0 })} />
        </Field>
        <Field label="Max height" htmlFor="pg-h">
          <NumInput id="pg-h" value={params.resize.maxHeight} min={16} max={8192} onChange={(v) => setResize({ maxHeight: v ?? 0 })} />
        </Field>
      </div>
      <Field label="Fit" htmlFor="pg-fit">
        <Select value={params.resize.fit} onValueChange={(v) => setResize({ fit: v })}>
          <SelectTrigger id="pg-fit" className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="inside">inside — keep aspect ratio</SelectItem>
            <SelectItem value="cover">cover — crop to fill</SelectItem>
          </SelectContent>
        </Select>
      </Field>
      <div className="flex flex-wrap gap-x-6 gap-y-2">
        <Toggle
          id="pg-gray"
          label="Grayscale"
          checked={params.color.grayscale}
          onChange={(v) => onChange({ ...params, color: { ...params.color, grayscale: v } })}
        />
        <Toggle
          id="pg-norm"
          label="Normalise"
          checked={params.color.normalise}
          onChange={(v) => onChange({ ...params, color: { ...params.color, normalise: v } })}
        />
      </div>

      <div className="grid gap-3 rounded-lg border p-3">
        <Field label="Quality mode" htmlFor="pg-mode">
          <Select value={q.mode} onValueChange={(v) => setQ({ mode: v as ImageParams["quality"]["mode"] })}>
            <SelectTrigger id="pg-mode" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="budget">budget — fit a byte budget</SelectItem>
              <SelectItem value="ssim">ssim — target similarity</SelectItem>
              <SelectItem value="fixed">fixed — constant quality</SelectItem>
            </SelectContent>
          </Select>
        </Field>
        {q.mode === "budget" && (
          <Field label="Max size (KB)" htmlFor="pg-maxb">
            <NumInput
              id="pg-maxb"
              value={q.maxBytes === undefined ? undefined : Math.round(q.maxBytes / 1024)}
              min={1}
              max={10240}
              onChange={(v) => setQ({ maxBytes: v === undefined ? undefined : Math.round(v * 1024) })}
            />
          </Field>
        )}
        {q.mode === "ssim" && (
          <Field label="Target SSIM" htmlFor="pg-target" hint="0.95–0.999 — higher keeps more detail.">
            <NumInput id="pg-target" value={q.target} min={0.8} max={1} step={0.001} onChange={(v) => setQ({ target: v })} />
          </Field>
        )}
        {q.mode === "fixed" && (
          <Field label="Quality (q)" htmlFor="pg-q">
            <NumInput id="pg-q" value={q.q} min={1} max={100} onChange={(v) => setQ({ q: v })} />
          </Field>
        )}
        {q.mode !== "fixed" && (
          <div className="grid grid-cols-2 gap-3">
            <Field label="q max" htmlFor="pg-qmax">
              <NumInput id="pg-qmax" value={q.qMax} min={1} max={100} onChange={(v) => setQ({ qMax: v })} />
            </Field>
            <Field label="q min" htmlFor="pg-qmin">
              <NumInput id="pg-qmin" value={q.qMin} min={1} max={100} onChange={(v) => setQ({ qMin: v })} />
            </Field>
          </div>
        )}
      </div>

      <fieldset className="grid gap-2">
        <legend className="mb-1 text-sm font-medium">Outputs</legend>
        <div className="flex flex-wrap gap-x-6 gap-y-2">
          {FORMATS.map((f) => (
            <Toggle key={f} id={`pg-out-${f}`} label={f.toUpperCase()} checked={hasFormat(f)} onChange={(v) => toggleFormat(f, v)} />
          ))}
        </div>
      </fieldset>
    </div>
  );
}

function BeforeAfter({ before, after }: { before: string; after: PlaygroundRendition }) {
  const [pos, setPos] = useState(50);
  const afterSrc = useMemo(() => `data:${after.content_type};base64,${after.data}`, [after]);
  const ratio = after.width / Math.max(1, after.height);
  return (
    <div>
      <div
        className="relative mx-auto overflow-hidden rounded-lg border bg-muted select-none"
        style={{ aspectRatio: String(ratio), width: `min(100%, calc(70vh * ${ratio}))` }}
      >
        <img src={before} alt="Original" className="absolute inset-0 size-full object-contain" draggable={false} />
        <img
          src={afterSrc}
          alt={`${after.name} ${after.format}`}
          className="absolute inset-0 size-full object-contain"
          style={{ clipPath: `inset(0 0 0 ${pos}%)` }}
          draggable={false}
        />
        <div className="pointer-events-none absolute inset-y-0 w-0.5 bg-white shadow-[0_0_0_1px_rgba(0,0,0,0.3)]" style={{ left: `${pos}%` }}>
          <div className="absolute top-1/2 left-1/2 size-7 -translate-x-1/2 -translate-y-1/2 rounded-full border bg-white shadow" />
        </div>
        <span className="pointer-events-none absolute top-2 left-2 rounded bg-black/60 px-1.5 py-0.5 text-[11px] text-white">Original</span>
        <span className="pointer-events-none absolute top-2 right-2 rounded bg-black/60 px-1.5 py-0.5 text-[11px] text-white">
          {after.format.toUpperCase()}
        </span>
        <input
          type="range"
          min={0}
          max={100}
          step={0.1}
          value={pos}
          onChange={(e) => setPos(Number(e.target.value))}
          aria-label="Before / after position"
          className="absolute inset-0 size-full cursor-ew-resize opacity-0"
        />
      </div>
    </div>
  );
}

function SaveDialog({
  open,
  onOpenChange,
  projectId,
  presets,
  baseSlug,
  params,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  projectId: string;
  presets: Preset[];
  baseSlug: string | undefined;
  params: ImageParams;
}) {
  const NEW = "__new";
  const [target, setTarget] = useState(baseSlug ?? NEW);
  const [newSlug, setNewSlug] = useState("");
  const [description, setDescription] = useState("");
  const save = useSavePresetVersion(projectId);
  const slug = target === NEW ? newSlug.trim() : target;
  const slugError = target === NEW && newSlug !== "" && !SLUG_RE.test(newSlug.trim()) ? "Lowercase letters, digits and dashes, starting with a letter (2–32)." : undefined;
  const fieldErr = isApiError(save.error) ? (save.error.fieldErrors.params ?? save.error.fieldErrors.slug) : undefined;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Save as new version</DialogTitle>
          <DialogDescription>
            Adds a version to an existing preset, or creates a new one. Existing assets keep the version they were processed with.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          <Field label="Preset" htmlFor="pg-save-slug">
            <Select value={target} onValueChange={setTarget}>
              <SelectTrigger id="pg-save-slug" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {presets.map((p) => (
                  <SelectItem key={p.slug} value={p.slug}>
                    {p.slug} <span className="text-xs text-muted-foreground">→ v{p.current_version + 1}</span>
                  </SelectItem>
                ))}
                <SelectItem value={NEW}>New preset…</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          {target === NEW && (
            <>
              <Field label="New preset slug" htmlFor="pg-save-new" error={slugError}>
                <Input id="pg-save-new" value={newSlug} onChange={(e) => setNewSlug(e.target.value)} placeholder="hero-banner" className="font-mono" />
              </Field>
              <Field label="Description" htmlFor="pg-save-desc">
                <Input id="pg-save-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
              </Field>
            </>
          )}
          {(fieldErr || save.isError) && (
            <p className="text-sm text-destructive" role="alert">
              {fieldErr ?? errorMessage(save.error)}
            </p>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            disabled={!slug || !!slugError || save.isPending}
            onClick={() =>
              save.mutate(
                { slug, params, description: target === NEW ? description.trim() || undefined : undefined },
                {
                  onSuccess: (p) => {
                    toast.success(`Saved ${p.slug} v${p.current_version}`);
                    onOpenChange(false);
                  },
                },
              )
            }
          >
            {save.isPending && <Loader2 className="animate-spin" />}
            Save version
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function Results({ result, before, params, projectId, presets, baseSlug }: {
  result: PlaygroundResult;
  before: string;
  params: ImageParams;
  projectId: string;
  presets: Preset[];
  baseSlug: string | undefined;
}) {
  const [picked, setPicked] = useState(0);
  const [saveOpen, setSaveOpen] = useState(false);
  const idx = Math.min(picked, result.renditions.length - 1);
  const sel = result.renditions[idx];

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="text-sm text-muted-foreground">
          Source {result.source.format.toUpperCase()} · {result.source.width}×{result.source.height} · {formatBytes(result.source.bytes)} ·
          processed in {formatMs(result.elapsed_ms)}
        </div>
        <Button onClick={() => setSaveOpen(true)}>
          <Save /> Save as new version
        </Button>
      </div>

      {sel && <BeforeAfter before={before} after={sel} />}

      <div className="overflow-hidden rounded-xl border bg-card">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b text-left text-xs text-muted-foreground">
              <th className="px-4 py-2 font-medium">Rendition</th>
              <th className="px-2 py-2 font-medium">Size</th>
              <th className="hidden px-2 py-2 font-medium sm:table-cell">Dimensions</th>
              <th className="px-2 py-2 font-medium">SSIM</th>
              <th className="hidden px-2 py-2 font-medium md:table-cell">Quality</th>
              <th className="hidden px-2 py-2 font-medium md:table-cell">Iterations</th>
              <th className="px-4 py-2 font-medium" />
            </tr>
          </thead>
          <tbody>
            {result.renditions.map((r, i) => (
              <tr
                key={r.name + r.format}
                className={`cursor-pointer border-b last:border-0 hover:bg-muted/40 ${i === idx ? "bg-muted/50" : ""}`}
                onClick={() => setPicked(i)}
              >
                <td className="px-4 py-2">
                  <label className="flex cursor-pointer items-center gap-2">
                    <input type="radio" name="pg-rendition" checked={i === idx} onChange={() => setPicked(i)} />
                    <span className="font-medium">{r.name}</span>
                    <span className="font-mono text-xs text-muted-foreground uppercase">{r.format}</span>
                  </label>
                </td>
                <td className="px-2 py-2 tabular-nums">
                  {formatBytes(r.bytes)}
                  {result.source.bytes > 0 && (
                    <span className="ml-1.5 text-xs text-success">−{Math.max(0, (1 - r.bytes / result.source.bytes) * 100).toFixed(0)}%</span>
                  )}
                </td>
                <td className="hidden px-2 py-2 tabular-nums sm:table-cell">
                  {r.width}×{r.height}
                </td>
                <td className="px-2 py-2 tabular-nums">{r.ssim > 0 ? r.ssim.toFixed(4) : "—"}</td>
                <td className="hidden px-2 py-2 tabular-nums md:table-cell">{r.quality}</td>
                <td className="hidden px-2 py-2 tabular-nums md:table-cell">{r.iterations}</td>
                <td className="px-4 py-2 text-right">
                  {r.over_budget && (
                    <StatusBadge tone="warning">
                      <AlertTriangle className="size-3" /> over budget
                    </StatusBadge>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {saveOpen && (
        <SaveDialog
          open
          onOpenChange={setSaveOpen}
          projectId={projectId}
          presets={presets}
          baseSlug={baseSlug}
          params={params}
        />
      )}
    </div>
  );
}

export default function PlaygroundPage() {
  const { project } = route.useSearch();
  const navigate = route.useNavigate();
  const setProject = useCallback((id: string) => void navigate({ search: { project: id }, replace: true }), [navigate]);
  const { projects, list, active } = useActiveProject(project, setProject);

  const presets = useQuery({ ...presetsQuery(active?.id ?? ""), enabled: !!active });
  const imagePresets = useMemo(() => (presets.data ?? []).filter((p) => p.kind === "image"), [presets.data]);

  const [baseSlug, setBaseSlug] = useState<string | undefined>();
  const [params, setParams] = useState<ImageParams>(() => normalize(undefined));
  const [file, setFile] = useState<File | null>(null);
  const [before, setBefore] = useState<string | null>(null);
  const [result, setResult] = useState<PlaygroundResult | null>(null);
  const [ranWith, setRanWith] = useState<ImageParams | null>(null);
  const [fileError, setFileError] = useState<string | null>(null);
  const urlRef = useRef<string | null>(null);

  useEffect(
    () => () => {
      if (urlRef.current) URL.revokeObjectURL(urlRef.current);
    },
    [],
  );

  // Pick the project's first image preset when presets load or the project changes.
  const firstSlug = imagePresets[0]?.slug;
  const [seeded, setSeeded] = useState<string | null>(null);
  const seedKey = `${active?.id ?? ""}:${firstSlug ?? ""}`;
  if (active && presets.isSuccess && seeded !== seedKey) {
    setSeeded(seedKey);
    const first = imagePresets[0];
    setBaseSlug(first?.slug);
    setParams(normalize(first?.params));
  }

  const run = useMutation({
    mutationFn: (v: { file: File; params: ImageParams }) => api.playground.run(v.file, v.params),
    onSuccess: (r, v) => {
      setResult(r);
      setRanWith(v.params);
    },
  });

  const onFile = (f: File | null) => {
    setFileError(null);
    if (!f) return;
    if (f.size > MAX_SAMPLE) {
      setFileError("The sample is limited to 25 MB.");
      return;
    }
    if (urlRef.current) URL.revokeObjectURL(urlRef.current);
    urlRef.current = URL.createObjectURL(f);
    setFile(f);
    setBefore(urlRef.current);
    setResult(null);
    run.reset();
  };

  const pickPreset = (slug: string) => {
    setBaseSlug(slug);
    setParams(normalize(imagePresets.find((p) => p.slug === slug)?.params));
  };

  const problem = isApiError(run.error) ? run.error : null;

  return (
    <>
      <PageHeader
        title="Playground"
        description="Try preset parameters on a sample image. Nothing is stored."
        actions={<ProjectSelect projects={list} value={active?.id} onChange={setProject} loading={projects.isPending} />}
      />

      {projects.isError ? (
        <ErrorState error={projects.error} onRetry={() => void projects.refetch()} />
      ) : projects.isSuccess && list.length === 0 ? (
        <EmptyState icon={<FolderKanban />} title="No projects yet" description="Create a project to experiment with its presets." />
      ) : (
        <div className="grid gap-6 lg:grid-cols-[22rem_1fr]">
          <Card className="h-fit gap-4 py-5">
            <CardHeader className="px-5">
              <CardTitle className="text-sm font-medium">Parameters</CardTitle>
            </CardHeader>
            <CardContent className="grid gap-4 px-5">
              <Field label="Sample image" htmlFor="pg-file" error={fileError ?? undefined}>
                <label
                  htmlFor="pg-file"
                  className="flex cursor-pointer items-center gap-2 rounded-md border border-dashed px-3 py-2 text-sm text-muted-foreground hover:bg-muted/50"
                >
                  <Upload className="size-4 shrink-0" />
                  <span className="truncate">{file ? `${file.name} · ${formatBytes(file.size)}` : "Choose an image (max 25 MB)"}</span>
                </label>
                <input
                  id="pg-file"
                  type="file"
                  accept="image/*"
                  className="sr-only"
                  onChange={(e) => {
                    onFile(e.target.files?.[0] ?? null);
                    e.target.value = "";
                  }}
                />
              </Field>
              <Field label="Base preset" htmlFor="pg-preset">
                <Select value={baseSlug ?? ""} onValueChange={pickPreset} disabled={imagePresets.length === 0}>
                  <SelectTrigger id="pg-preset" className="w-full">
                    <SelectValue placeholder={presets.isPending ? "Loading…" : "No image presets"} />
                  </SelectTrigger>
                  <SelectContent>
                    {imagePresets.map((p) => (
                      <SelectItem key={p.slug} value={p.slug}>
                        {p.slug} <span className="text-xs text-muted-foreground">v{p.current_version}</span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <ParamsForm params={params} onChange={setParams} />
              <Button disabled={!file || run.isPending} onClick={() => file && run.mutate({ file, params })}>
                {run.isPending ? <Loader2 className="animate-spin" /> : <Play />}
                {run.isPending ? "Processing…" : "Run"}
              </Button>
            </CardContent>
          </Card>

          <div className="min-w-0">
            {run.isError ? (
              <ErrorState
                title="Processing failed"
                error={run.error}
                onRetry={file ? () => run.mutate({ file, params }) : undefined}
                className="mb-4"
              />
            ) : null}
            {problem?.fieldErrors.params && <p className="mb-4 text-sm text-destructive">{problem.fieldErrors.params}</p>}
            {result && before && ranWith ? (
              <Results
                result={result}
                before={before}
                params={ranWith}
                projectId={active?.id ?? ""}
                presets={imagePresets}
                baseSlug={baseSlug}
              />
            ) : !run.isError ? (
              <EmptyState
                icon={run.isPending ? <Loader2 className="animate-spin" /> : <FlaskConical />}
                title={run.isPending ? "Processing sample…" : "Run a sample"}
                description={
                  run.isPending
                    ? "Encoding every output; budget and SSIM modes search for the best quality."
                    : "Upload an image, tweak the parameters and press Run to compare the original with each rendition."
                }
              />
            ) : null}
          </div>
        </div>
      )}
    </>
  );
}
