/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** "1" enables the in-memory mock API (dev only). */
  readonly VITE_MOCK?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
