/* eslint-disable react-refresh/only-export-components -- hooks and a boundary component live together by design */
import { useCallback, useSyncExternalStore, type ReactNode } from "react";

import type { Locale, Params } from "./core";
import type { MessageKey } from "./dictionaries";
import { getLocale, setLocale, subscribeLocale, t as translate } from "./store";

/** Active locale; re-renders on change. */
export function useLocale(): [Locale, (l: Locale) => void] {
  const locale = useSyncExternalStore(subscribeLocale, getLocale, getLocale);
  return [locale, setLocale];
}

/** Typed translate function bound to the active language; re-renders on change. */
export function useT(): (key: MessageKey, params?: Params) => string {
  const locale = useSyncExternalStore(subscribeLocale, getLocale, getLocale);
  // eslint-disable-next-line react-hooks/exhaustive-deps -- identity must change with the locale
  return useCallback((key: MessageKey, params?: Params) => translate(key, params), [locale]);
}

/**
 * Remounts its subtree when the language changes, so every component (including
 * ones that call lib/format.ts or the global `t` directly) renders in the new language.
 */
export function LocaleBoundary({ children }: { children: ReactNode }) {
  const locale = useSyncExternalStore(subscribeLocale, getLocale, getLocale);
  return <LocaleKey key={locale}>{children}</LocaleKey>;
}

function LocaleKey({ children }: { children: ReactNode }) {
  return <>{children}</>;
}
