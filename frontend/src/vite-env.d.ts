/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Base URL of the Yearning API; empty string means same-origin deployment. */
  readonly VITE_API_BASE_URL?: string;
  /** Enables the MSW browser worker for mock-driven development and e2e. */
  readonly VITE_ENABLE_MOCK?: string;
  /** Enables the public static interactive demo experience (Cloudflare Pages). */
  readonly VITE_DEMO_MODE?: string;
}

/** Owner-mandated footer links, inlined by the vite.config.ts define. */
declare const __SPONSOR_URL__: string;
declare const __DOCS_URL__: string;
declare const __SOURCE_URL__: string;

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
