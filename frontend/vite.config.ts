import { defineConfig } from "vite";
import type { Connect, Plugin } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { readFileSync } from "node:fs";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import path from "node:path";

// Yearning v4 frontend. Production builds must not reach the network:
// dependencies are fully resolved from the lockfile at install time and the
// shadcn registry, the Shadcn Dashboard MCP and the frozen template checkout
// are development-time inputs only (code-generation-policy.json).

// Home release banner (RCP-20261020-RELEASE-FEED-BACKEND): the client reads
// the backend's /releases/latest snapshot; the server-side refresher is the
// only component that talks to the upstream GitHub API. The former
// __RELEASE_LATEST_URL__ build-time injection is gone with the browser
// direct-fetch design.

// Owner-mandated footer links (2026-09-02). Inlined here so src stays free
// of absolute-URL literals.
const footerLinks = {
  __SPONSOR_URL__: JSON.stringify(
    process.env.VITE_SPONSOR_URL ?? "https://next.yearning.io/zh/about/w5jt71jw/",
  ),
  __DOCS_URL__: JSON.stringify(process.env.VITE_DOCS_URL ?? "https://next.yearning.io/"),
  __SOURCE_URL__: JSON.stringify(
    process.env.VITE_SOURCE_URL ?? "https://github.com/cookieY/Yearning",
  ),
};

// SEC-0032: the MSW service worker is dev/test tooling and must never ship in
// a release dist. Everything under public/ is copied into every build
// unconditionally, so the worker lives at src/shared/mock/mockServiceWorker.js
// and this plugin is its only route to a URL:
//   - dev server: served at /mockServiceWorker.js regardless of flags (the
//     browser only registers it when main.tsx enables the mock world);
//   - build: emitted into the bundle only when the build opts into the mock
//     world (VITE_ENABLE_MOCK=true — the e2e webServer build). Release
//     builds keep the flag off and get neither the file nor any reference.
const MSW_WORKER_URL = "/mockServiceWorker.js";
const MSW_WORKER_SOURCE = path.resolve(__dirname, "src/shared/mock/mockServiceWorker.js");

// FE-F12 real-backend e2e proxy target (VITE_E2E_API_PROXY).
const e2eApiProxyTarget = process.env.VITE_E2E_API_PROXY;

function mswWorkerPlugin(): Plugin {
  return {
    name: "msw-worker-dev-only",
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        if (req.method !== "GET" || (req.url ?? "").split("?")[0] !== MSW_WORKER_URL) {
          next();
          return;
        }
        res.setHeader("Content-Type", "application/javascript");
        res.end(readFileSync(MSW_WORKER_SOURCE));
      });
    },
    generateBundle() {
      if (process.env.VITE_ENABLE_MOCK === "true") {
        this.emitFile({
          type: "asset",
          fileName: "mockServiceWorker.js",
          source: readFileSync(MSW_WORKER_SOURCE, "utf8"),
        });
      }
      if (process.env.VITE_DEMO_MODE === "true") {
        // Cloudflare Workers Static Assets caching headers for the public
        // pure-frontend interactive demo build (SPA fallback is handled by
        // wrangler.jsonc `not_found_handling: "single-page-application"`, so
        // emitting `_redirects` with `/* /index.html 200` would trigger
        // Cloudflare Workers code 100324). Never emitted in release dist.
        this.emitFile({
          type: "asset",
          fileName: "_headers",
          source:
            "/mockServiceWorker.js\n" +
            "  Cache-Control: no-cache\n" +
            "  Service-Worker-Allowed: /\n",
        });
      }
    },
  };
}

// MSW multi-tab Option 1 (owner ruling 2026-11-04): the dev server runs the mock
// world in-process (msw/node via src/shared/mock/node.ts), so every tab,
// browser and LAN device shares ONE authoritative world — the per-tab browser
// workers randomly swapped worlds through the shared service worker.
// The browser worker stays for `vite preview`/mock e2e builds and can be
// restored for dev debugging with VITE_MOCK_TRANSPORT=worker.
const MOCK_NODE_ENTRY = "/src/shared/mock/node.ts";
const MOCK_SCENARIOS_ENTRY = "/src/shared/mock/scenarios.ts";
const MOCK_SCENARIO_STORE_ENTRY = "/src/shared/mock/scenario-store.ts";
const MOCK_DIMENSIONS_ENTRY = "/src/shared/mock/server-dimensions.ts";
const DIMENSIONS_ENDPOINT = "/__mock/dimensions";

function readRequestBody(req: Connect.IncomingMessage): Promise<string> {
  return new Promise((resolve) => {
    let data = "";
    req.on("data", (chunk: Buffer) => {
      data += chunk.toString("utf8");
    });
    req.on("end", () => {
      resolve(data);
    });
  });
}

// Loopback sink singleton (process-wide): msw answers matched requests on
// the way out; unmatched ones reach this plain 404 server and the middleware
// falls through to vite. 1 MiB maxHeaderSize: msw 2.15's bypass of unmatched
// requests re-serializes the forwarded headers far above node's 16 KiB
// default, tripping HPE_HEADER_OVERFLOW (surfaced as 431 to the client).
const SINK_KEY = Symbol.for("yearning.mock.node-sink");
const NO_HANDLER_HEADER = "x-mock-no-handler";

function ensureSink(): Promise<string> {
  const globalTarget = globalThis as typeof globalThis & Record<symbol, unknown>;
  const existing = globalTarget[SINK_KEY];
  if (typeof existing === "string") return Promise.resolve(existing);
  const sink = createServer(
    { maxHeaderSize: 1024 * 1024 },
    (_req, res) => {
      res.statusCode = 404;
      res.setHeader(NO_HANDLER_HEADER, "1");
      res.end();
    },
  );
  // msw's bypass re-emission can still overflow in pathological cases; keep
  // the socket cleanup silent instead of node's default 400 noise.
  sink.on("clientError", () => {});
  return new Promise<string>((resolve, reject) => {
    sink.once("error", reject);
    sink.listen(0, "127.0.0.1", () => {
      const address = sink.address() as AddressInfo;
      const sinkUrl = `http://127.0.0.1:${String(address.port)}`;
      globalTarget[SINK_KEY] = sinkUrl;
      resolve(sinkUrl);
    });
  });
}
const HOP_BY_HOP = new Set([
  "connection",
  "keep-alive",
  "transfer-encoding",
  "upgrade",
  "content-encoding",
  "content-length",
]);

function viteOwnedPath(url: string): boolean {
  const path = url.split("?")[0] ?? url;
  return (
    path === "/" ||
    path.startsWith("/@") ||
    path.startsWith("/__") ||
    path.startsWith("/node_modules/") ||
    path.startsWith("/src/") ||
    path === "/mockServiceWorker.js" ||
    /\.[^/]+$/.test(path)
  );
}

function readRequestBodyBuffer(req: IncomingMessage): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    req.on("data", (chunk: Buffer) => {
      chunks.push(chunk);
    });
    req.on("end", () => {
      resolve(Buffer.concat(chunks));
    });
    req.on("error", reject);
  });
}

async function startDispatcher() {
  const sinkUrl = await ensureSink();
  const dispatch = async (req: IncomingMessage, res: ServerResponse): Promise<boolean> => {
    const headers = new Headers();
    for (const [name, value] of Object.entries(req.headers)) {
      // host/connection are per-hop; content-length is recomputed from
      // the buffered body.
      if (value === undefined || name === "host" || name === "connection" || name === "content-length") {
        continue;
      }
      for (const entry of Array.isArray(value) ? value : [value]) {
        headers.append(name, entry);
      }
    }
    const hasBody = req.method !== undefined && req.method !== "GET" && req.method !== "HEAD";
    const body = hasBody ? await readRequestBodyBuffer(req) : undefined;
    try {
      const response = await fetch(`${sinkUrl}${req.url ?? "/"}`, {
        method: req.method,
        headers,
        body: body !== undefined && body.length > 0 ? new Uint8Array(body) : undefined,
        redirect: "manual",
      });
      if (response.headers.get(NO_HANDLER_HEADER) === "1") {
        if ((req.url ?? "").startsWith("/api/")) {
          res.statusCode = 500;
          res.setHeader("Content-Type", "application/problem+json");
          res.end(
            JSON.stringify({
              type: "about:blank",
              title: "mock_unhandled_request",
              status: 500,
              detail: `No mock handler matched ${req.method ?? "GET"} ${req.url ?? ""}`,
            }),
          );
          return true;
        }
        return false;
      }
      res.statusCode = response.status;
      response.headers.forEach((value, name) => {
        if (!HOP_BY_HOP.has(name) && name !== "set-cookie") res.setHeader(name, value);
      });
      for (const cookie of response.headers.getSetCookie()) res.appendHeader("set-cookie", cookie);
      // Mock handlers return buffered JSON responses, never streams.
      res.end(Buffer.from(await response.arrayBuffer()));
      return true;
    } catch (error) {
      if ((req.url ?? "").startsWith("/api/")) {
        res.statusCode = 500;
        res.setHeader("Content-Type", "application/problem+json");
        res.end(
          JSON.stringify({
            type: "about:blank",
            title: "mock_dispatch_error",
            status: 500,
            detail: error instanceof Error ? error.message : String(error),
          }),
        );
        return true;
      }
      // Loopback hiccup — let vite answer non-API routes rather than 500 the tab.
      return false;
    }
  };
  return {
    dispatch,
  };
}

// Shape inferred from startDispatcher (no hand-written function type: named
// type-position parameters trip this setup's no-unused-vars).
type MockDispatcher = Awaited<ReturnType<typeof startDispatcher>>;

function mockNodePlugin(): Plugin {
  return {
    name: "mock-node-dev-only",
    apply: "serve",
    configureServer(server) {
      if (process.env.VITE_ENABLE_MOCK !== "true") return;
      let dispatcher: MockDispatcher | null = null;
      // Boot/refresh the in-process mock world. The setupServer singleton
      // (node.ts) survives vite restarts; fixture edits and restarts load
      // FRESH handler modules and `resetHandlers` swaps them into the one
      // live server — the world resets like the old per-tab worker did on
      // reload, while the msw cookie jar (msw's own module cache) keeps
      // sessions alive like a backend surviving a config reload.
      const boot = async (): Promise<void> => {
        try {
          const nodeMod = await server.ssrLoadModule(MOCK_NODE_ENTRY);
          const scenariosMod = await server.ssrLoadModule(MOCK_SCENARIOS_ENTRY);
          const scenarioStoreMod = await server.ssrLoadModule(MOCK_SCENARIO_STORE_ENTRY);
          const mswServer = nodeMod.ensureMockNodeServer();
          mswServer.resetHandlers(...scenariosMod.baseHandlers());
          scenariosMod.applyScenarioToServer(
            mswServer,
            scenarioStoreMod.readStoredScenario(),
          );
          dispatcher = await startDispatcher();
        } catch (error) {
          server.config.logger.error(`[mock-node] failed to start: ${String(error)}`);
        }
      };
      void boot();
      // Fixture edits restart the in-memory world — the same reset a page
      // reload gave the old worker world.
      server.watcher.on("change", (file) => {
        if (
          file.includes(`${path.sep}src${path.sep}shared${path.sep}mock${path.sep}`) ||
          file.endsWith("settings-bridge.ts")
        ) {
          server.moduleGraph.invalidateAll();
          void boot();
        }
      });
      // Dev-only dimension control (identity / scenario / auth behavior): GET
      // reads, POST `{identity?, scenario?, authBehavior?}` sets. Readers
      // without localStorage (the node world) answer from this state; tabs
      // sync it on boot (client-dimensions.ts).
      server.middlewares.use(DIMENSIONS_ENDPOINT, async (req, res) => {
        const dimensions = await server.ssrLoadModule(MOCK_DIMENSIONS_ENTRY);
        if (req.method === "GET") {
          res.setHeader("Content-Type", "application/json");
          res.end(JSON.stringify(dimensions.getServerDimensions()));
          return;
        }
        if (req.method === "POST") {
          const body = await readRequestBody(req);
          res.setHeader("Content-Type", "application/json");
          try {
            const patch = JSON.parse(body) as Record<string, unknown>;
            res.end(
              JSON.stringify(
                dimensions.setServerDimensions({
                  identity: typeof patch.identity === "string" ? patch.identity : undefined,
                  scenario: typeof patch.scenario === "string" ? patch.scenario : undefined,
                  authBehavior:
                    typeof patch.authBehavior === "string" ? patch.authBehavior : undefined,
                }),
              ),
            );
          } catch {
            res.statusCode = 400;
            res.end("{}");
          }
          return;
        }
        res.statusCode = 405;
        res.end();
      });
      // Bridge every non-vite request into the in-process mock world; the
      // dispatcher answers `true` when a handler matched and the response is
      // already written, `false` when vite must answer (SPA routes, unknown
      // paths). Registered inside configureServer so it runs before vite's
      // own middlewares — the skip list above keeps vite's requests out.
      // Browser document navigations (full loads / refreshes of SPA routes)
      // must reach vite's SPA fallback: several mock handlers use route-like
      // wildcard patterns (`*/admin/users`, `*/admin/datasources`) that also
      // match the page URL, and without this guard a refresh on those routes
      // renders raw JSON instead of the app. App fetches carry
      // sec-fetch-dest: empty / accept: application/json and still dispatch.
      server.middlewares.use(async (req, res, next) => {
        if (dispatcher === null || req.url === undefined || viteOwnedPath(req.url)) {
          next();
          return;
        }
        const fetchDest = req.headers["sec-fetch-dest"];
        const accept = req.headers.accept;
        if (
          fetchDest === "document" ||
          (typeof accept === "string" && accept.startsWith("text/html"))
        ) {
          next();
          return;
        }
        const handled = await dispatcher.dispatch(req, res).catch(() => false);
        if (!handled) next();
      });
    },
  };
}

export default defineConfig({
  plugins: [react(), tailwindcss(), mswWorkerPlugin(), mockNodePlugin()],
  define: {
    ...footerLinks,
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  build: {
    sourcemap: false,
    // Shared dist protection (2026-10-19 integration incident): e2e webServer previously rebuilt shared dist/
    // as a mock build, overwriting in real time the artifacts served by manual preview (4273) — operators
    // fell into the fake MSW world upon refresh. E2E builds always use a separate directory; dist/ belongs strictly to explicit builds.
    outDir: process.env.VITE_BUILD_OUT_DIR ?? "dist",
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks: undefined,
      },
    },
  },
  server: {
    port: 5173,
    // owner 2026-11-04: allow LAN access in dev (mobile/other devices open
    // http://<lan-ip>:5173 via local LAN IP). host:true binds all interfaces; Vite default
    // host check only allows IP and localhost — custom hostnames require allowedHosts.
    host: true,
    strictPort: true,
    proxy: e2eApiProxyTarget
      ? {
          "/api": {
            target: e2eApiProxyTarget,
            changeOrigin: false,
          },
        }
      : undefined,
  },
  // FE-F12 real-backend e2e: `vite preview` proxies /api to the local serve
  // stack when VITE_E2E_API_PROXY is set (playwright.real.config.ts). The
  // default config — mock e2e, dev, release — never sets the variable and
  // keeps no proxy.
  //
  // SEC-0004 / F-ENV-002: keep changeOrigin: false so the proxied request
  // preserves the browser-facing Host header. That keeps Host aligned with
  // the browser's Origin/Referer for RequireSameOrigin AND ensures OIDC
  // redirect_uri construction (c.Request.Host) points back to the frontend
  // origin rather than the internal backend loopback port.
  preview: e2eApiProxyTarget
      ? {
          proxy: {
            "/api": {
              target: e2eApiProxyTarget,
              changeOrigin: false,
            },
          },
        }
      : undefined,
});
