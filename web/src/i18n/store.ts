// Framework-free locale store so non-React code (toasts in query hooks, lib/format.ts)
// can translate and format with the active language.
import { createTranslator, DEFAULT_LOCALE, detectLocale, isLocale, type Locale, type Params, type Translator } from "./core";
import { dictionaries, type MessageKey } from "./dictionaries";

const STORAGE_KEY = "kadr-lang";

function readStored(): Locale | null {
  try {
    const v = localStorage.getItem(STORAGE_KEY);
    return isLocale(v) ? v : null;
  } catch {
    return null;
  }
}

function initial(): Locale {
  if (typeof navigator === "undefined") return DEFAULT_LOCALE;
  return readStored() ?? detectLocale(navigator.languages?.length ? navigator.languages : [navigator.language]);
}

let current: Locale = initial();
let translator: Translator<MessageKey> = build(current);
const listeners = new Set<() => void>();

function build(locale: Locale): Translator<MessageKey> {
  return createTranslator<MessageKey>(dictionaries, locale, {
    onMissing: (key, l) => {
      if (import.meta.env.DEV) console.warn(`[i18n] missing "${l}" translation for "${key}", falling back to English`);
    },
  });
}

function applyDocument(locale: Locale) {
  if (typeof document !== "undefined") document.documentElement.lang = locale;
}
applyDocument(current);

export function getLocale(): Locale {
  return current;
}

export function setLocale(locale: Locale) {
  if (locale === current) return;
  current = locale;
  translator = build(locale);
  try {
    localStorage.setItem(STORAGE_KEY, locale);
  } catch {
    /* storage unavailable */
  }
  applyDocument(locale);
  listeners.forEach((l) => l());
}

export function subscribeLocale(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

/** Translate with the active language (usable outside React). */
export function t(key: MessageKey, params?: Params): string {
  return translator(key, params);
}

/** BCP-47 tag of the active language for Intl APIs. */
export function intlLocale(): string {
  return current;
}
