import { setupServer, type SetupServer } from "msw/node";

/**
 * Node transport for the mock world (owner ruling 2026-11-04, MSW multi-tab
 * Option 1): booted by the vite dev-server plugin (vite.config.ts), it gives
 * every tab, browser and LAN device ONE authoritative in-memory world instead
 * of the per-tab browser workers. Vitest keeps its own in-process servers;
 * `vite preview` / mock e2e builds keep the browser worker (browser.ts).
 *
 * The setupServer is a PROCESS-LEVEL SINGLETON guarded on globalThis:
 * msw 2.15's experimental network layer does not reliably detach its
 * interceptors on close(), so every fresh instance kept answering requests
 * alongside the older ones (duplicate handler resolutions, already-consumed
 * bodies). The dev server instead refreshes the ONE live server through
 * `resetHandlers` whenever fixtures change — see mockNodePlugin in
 * vite.config.ts, which loads the handler modules via ssrLoadModule and
 * feeds `baseHandlers()` into the server returned by
 * `ensureMockNodeServer`. The msw cookie jar and interceptors
 * live in msw's own (externalized) module cache, so sessions survive those
 * refreshes exactly like a backend surviving a config reload.
 *
 * This module stays free of node built-ins on purpose: it lives in src/, is
 * type-checked by the app tsconfig (no node types), and is loaded by the dev
 * server through `server.ssrLoadModule`. msw/node intercepts OUTGOING
 * ClientRequests — the connect middleware in vite.config.ts bridges incoming
 * dev-server requests onto that surface via a loopback sink. Every handler
 * path is an asterisk wildcard, so the loopback host/port never affects
 * matching.
 */

const SERVER_KEY = Symbol.for("yearning.mock.node-server");

interface GlobalWithMockServer {
  [SERVER_KEY]?: SetupServer;
}

/** Create-or-return the one node mock server for this process. */
export function ensureMockNodeServer(): SetupServer {
  const globalTarget = globalThis as typeof globalThis & GlobalWithMockServer;
  if (globalTarget[SERVER_KEY] === undefined) {
    const server = setupServer();
    server.listen({
      onUnhandledRequest(request, print) {
        const pathname = new URL(request.url).pathname;
        if (pathname.startsWith("/api/")) {
          print.error();
          throw new Error(`Unhandled API mock request: ${request.method} ${pathname}`);
        }
      },
    });
    globalTarget[SERVER_KEY] = server;
  }
  return globalTarget[SERVER_KEY];
}
