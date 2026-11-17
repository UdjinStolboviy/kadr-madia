import { describe, expect, it, vi } from "vitest";

import { createTranslator, detectLocale, formatMessage, LOCALES } from "./core";
import { namespaces } from "./namespaces";

const filesUk = "{count, plural, one {# файл} few {# файли} many {# файлів} other {# файлу}}";
const filesEs = "{count, plural, =0 {Sin archivos} one {# archivo} other {# archivos}}";

describe("plurals", () => {
  it("handles the four Ukrainian forms", () => {
    const f = (n: number) => formatMessage(filesUk, { count: n }, "uk");
    expect(f(1)).toBe("1 файл");
    expect(f(21)).toBe("21 файл");
    expect(f(2)).toBe("2 файли");
    expect(f(24)).toBe("24 файли");
    expect(f(5)).toBe("5 файлів");
    expect(f(11)).toBe("11 файлів");
    expect(f(12)).toBe("12 файлів");
    expect(f(1.5)).toBe("1,5 файлу");
  });
  it("handles Spanish and exact matches", () => {
    expect(formatMessage(filesEs, { count: 0 }, "es")).toBe("Sin archivos");
    expect(formatMessage(filesEs, { count: 1 }, "es")).toBe("1 archivo");
    expect(formatMessage(filesEs, { count: 3 }, "es")).toBe("3 archivos");
  });
  it("formats the # with the locale", () => {
    expect(formatMessage("{n, plural, other {# items}}", { n: 1234 }, "en")).toBe("1,234 items");
  });
});

describe("interpolation", () => {
  it("replaces params, leaves missing ones empty and supports number", () => {
    expect(formatMessage("Hi {name}, {n, number}!", { name: "A", n: 1500 }, "en")).toBe("Hi A, 1,500!");
    expect(formatMessage("Hi {name}!", {}, "en")).toBe("Hi !");
  });
  it("supports nested placeholders in plural branches and escapes", () => {
    expect(formatMessage("{n, plural, one {{who} has # key} other {{who} has # keys}}", { n: 2, who: "Bo" }, "en")).toBe(
      "Bo has 2 keys",
    );
    expect(formatMessage("a \\{literal\\} b", undefined, "en")).toBe("a {literal} b");
  });
});

describe("translator", () => {
  it("falls back to English and reports a missing key once", () => {
    const onMissing = vi.fn();
    const t = createTranslator({ en: { a: "A {x}", b: "B" }, uk: { a: "А {x}" }, es: {} }, "uk", { onMissing });
    expect(t("a", { x: 1 })).toBe("А 1");
    expect(t("b")).toBe("B");
    expect(t("b")).toBe("B");
    expect(onMissing).toHaveBeenCalledTimes(1);
    expect(t("zzz")).toBe("zzz");
  });
});

describe("detectLocale", () => {
  it("maps navigator languages", () => {
    expect(detectLocale(["uk-UA"])).toBe("uk");
    expect(detectLocale(["fr-FR", "es-MX"])).toBe("es");
    expect(detectLocale(["de"])).toBe("en");
    expect(detectLocale(undefined)).toBe("en");
  });
});

describe("dictionaries", () => {
  for (const [ns, mod] of Object.entries(namespaces)) {
    it(`${ns}: uk and es have exactly the English keys, prefixed "${ns}."`, () => {
      const enKeys = Object.keys(mod.en).sort();
      expect(Object.keys(mod.uk).sort()).toEqual(enKeys);
      expect(Object.keys(mod.es).sort()).toEqual(enKeys);
      for (const k of enKeys) expect(k.startsWith(`${ns}.`)).toBe(true);
    });
  }
  it("uses the same placeholders in every language", () => {
    const names = (s: string) => [...s.matchAll(/\{(\w+)(?:,|\})/g)].map((m) => m[1]).sort().join(",");
    for (const [ns, mod] of Object.entries(namespaces)) {
      for (const [k, v] of Object.entries(mod.en as Record<string, string>)) {
        for (const l of LOCALES) {
          const other = (mod[l] as Record<string, string>)[k]!;
          expect(names(other), `${ns} ${k} (${l})`).toBe(names(v));
        }
      }
    }
  });
  it("has no key collisions between namespaces", () => {
    const all = Object.values(namespaces).flatMap((m) => Object.keys(m.en));
    expect(new Set(all).size).toBe(all.length);
  });
});
