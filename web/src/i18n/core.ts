// Pure i18n core: no React, no DOM. Unit-tested in core.test.ts.
//
// Message syntax (ICU-lite):
//   {name}                                   simple interpolation
//   {n, plural, =0 {none} one {# file} few {# files} other {# files}}
//   {n, number}                              locale number formatting
// `#` inside a plural branch is the (formatted) count. Branches may nest
// further {placeholders}. Escape literal braces with a backslash: \{ \}.

export const LOCALES = ["en", "uk", "es"] as const;
export type Locale = (typeof LOCALES)[number];
export const DEFAULT_LOCALE: Locale = "en";

export const LOCALE_LABELS: Record<Locale, string> = {
  en: "English",
  uk: "Українська",
  es: "Español",
};

/** Compile-time guard: a translation must have exactly the keys of the English source. */
export type Messages<T> = { [K in keyof T]: string };

export type Params = Record<string, string | number | null | undefined>;
export type Dict = Record<string, string>;

export function isLocale(v: unknown): v is Locale {
  return typeof v === "string" && (LOCALES as readonly string[]).includes(v);
}

/** Picks the first supported language from a navigator.languages-style list. */
export function detectLocale(languages: readonly string[] | undefined | null): Locale {
  for (const l of languages ?? []) {
    const base = l.toLowerCase().split("-")[0];
    if (isLocale(base)) return base;
  }
  return DEFAULT_LOCALE;
}

// ---- Parser -----------------------------------------------------------------

type Node =
  | { t: "text"; v: string }
  | { t: "arg"; name: string }
  | { t: "number"; name: string }
  | { t: "plural"; name: string; branches: Record<string, Node[]> }
  | { t: "hash" };

const cache = new Map<string, Node[]>();

function parse(src: string): Node[] {
  const hit = cache.get(src);
  if (hit) return hit;
  let i = 0;

  function parseNodes(inPlural: boolean, stopAtBrace: boolean): Node[] {
    const out: Node[] = [];
    let buf = "";
    const flush = () => {
      if (buf) out.push({ t: "text", v: buf });
      buf = "";
    };
    while (i < src.length) {
      const c = src[i]!;
      if (c === "\\" && (src[i + 1] === "{" || src[i + 1] === "}")) {
        buf += src[i + 1];
        i += 2;
      } else if (c === "}" && stopAtBrace) {
        break;
      } else if (c === "{") {
        flush();
        i++;
        out.push(parseArg());
      } else if (c === "#" && inPlural) {
        flush();
        out.push({ t: "hash" });
        i++;
      } else {
        buf += c;
        i++;
      }
    }
    flush();
    return out;
  }

  function readUntil(chars: string): string {
    const start = i;
    while (i < src.length && !chars.includes(src[i]!)) i++;
    return src.slice(start, i).trim();
  }

  function parseArg(): Node {
    const name = readUntil(",}");
    if (src[i] === "}") {
      i++;
      return { t: "arg", name };
    }
    i++; // ,
    const kind = readUntil(",}");
    if (kind === "number") {
      if (src[i] === ",") readUntil("}");
      i++;
      return { t: "number", name };
    }
    if (kind !== "plural") throw new Error(`i18n: unsupported argument type "${kind}" in "${src}"`);
    i++; // ,
    const branches: Record<string, Node[]> = {};
    for (;;) {
      while (src[i] === " " || src[i] === "\n") i++;
      if (i >= src.length) throw new Error(`i18n: unterminated plural in "${src}"`);
      if (src[i] === "}") {
        i++;
        break;
      }
      const sel = readUntil(" {");
      while (src[i] === " ") i++;
      if (src[i] !== "{") throw new Error(`i18n: expected "{" after "${sel}" in "${src}"`);
      i++;
      branches[sel] = parseNodes(true, true);
      i++; // closing }
    }
    return { t: "plural", name, branches };
  }

  const nodes = parseNodes(false, false);
  cache.set(src, nodes);
  return nodes;
}

// ---- Formatter --------------------------------------------------------------

const pluralRules = new Map<string, Intl.PluralRules>();
function pluralCategory(locale: string, n: number): Intl.LDMLPluralRule {
  let pr = pluralRules.get(locale);
  if (!pr) {
    pr = new Intl.PluralRules(locale);
    pluralRules.set(locale, pr);
  }
  return pr.select(n);
}

function render(nodes: Node[], params: Params, locale: string, hash?: number): string {
  let out = "";
  for (const n of nodes) {
    switch (n.t) {
      case "text":
        out += n.v;
        break;
      case "hash":
        out += new Intl.NumberFormat(locale).format(hash ?? 0);
        break;
      case "arg": {
        const v = params[n.name];
        out += v === undefined || v === null ? "" : String(v);
        break;
      }
      case "number": {
        const v = Number(params[n.name]);
        out += Number.isFinite(v) ? new Intl.NumberFormat(locale).format(v) : "";
        break;
      }
      case "plural": {
        const v = Number(params[n.name]);
        const count = Number.isFinite(v) ? v : 0;
        const branch =
          n.branches[`=${count}`] ?? n.branches[pluralCategory(locale, count)] ?? n.branches["other"] ?? [];
        out += render(branch, params, locale, count);
        break;
      }
    }
  }
  return out;
}

export function formatMessage(template: string, params: Params | undefined, locale: string): string {
  // Fast path: nothing to interpolate.
  if (!template.includes("{") && !template.includes("\\")) return template;
  return render(parse(template), params ?? {}, locale);
}

// ---- Translator ---------------------------------------------------------------

export type Translator<K extends string = string> = (key: K, params?: Params) => string;

export interface TranslatorOptions {
  /** Called once per (locale,key) when the key is missing in the active dictionary. */
  onMissing?: (key: string, locale: Locale) => void;
}

export function createTranslator<K extends string>(
  dicts: Record<Locale, Dict>,
  locale: Locale,
  opts: TranslatorOptions = {},
): Translator<K> {
  const active = dicts[locale];
  const fallback = dicts[DEFAULT_LOCALE];
  const reported = new Set<string>();
  return (key, params) => {
    let msg = active[key];
    if (msg === undefined) {
      if (!reported.has(key)) {
        reported.add(key);
        opts.onMissing?.(key, locale);
      }
      msg = fallback[key];
      if (msg === undefined) return key;
    }
    return formatMessage(msg, params, locale);
  };
}
