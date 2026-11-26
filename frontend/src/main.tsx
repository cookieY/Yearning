import { StrictMode, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { AppProviders } from "@/app/providers/app-providers";
import { AppRouter } from "@/app/router/app-router";
import "@/app/styles/global.css";

async function bootstrap(): Promise<void> {
  const rootElement = document.getElementById("root");
  if (rootElement === null) {
    throw new Error("root element missing from index.html");
  }
  // The MSW worker must own the network before the first query fires
  // (code-generation-policy.json mock_layer): a request that escapes to the
  // real server would be judged anonymous and flip the app to /login.
  if (import.meta.env.VITE_ENABLE_MOCK === "true") {
    // Option 1 (owner 2026-11-04): on the vite dev server the mock world runs
    // in-process (msw/node middleware) and every tab shares it. The browser
    // worker stays for preview/e2e builds and VITE_MOCK_TRANSPORT=worker.
    const nodeTransport =
      import.meta.env.DEV && import.meta.env.VITE_MOCK_TRANSPORT !== "worker";
    if (nodeTransport) {
      const client = await import("@/shared/mock/client-dimensions");
      // Remove any previously registered worker so it cannot answer first,
      // then pull the shared dimensions so this tab's client-side gating
      // matches the server world.
      await client.unregisterMockWorker();
      await client.syncDimensionsFromServer();
    } else {
      const module = await import("@/shared/mock/browser");
      await module.startMockWorker();
    }
  } else if (
    import.meta.env.DEV &&
    typeof navigator !== "undefined" &&
    "serviceWorker" in navigator
  ) {
    const registrations = await navigator.serviceWorker.getRegistrations();
    await Promise.all(
      registrations.map((registration) => registration.unregister()),
    );
  }
  // Dev-only UI annotation helper (RCP-20261103-FE-AGENTATION-DEV-TOOL).
  // import.meta.env.DEV is statically false in production builds, so Vite
  // drops this branch and the package never reaches dist/. No endpoint is
  // configured: annotations are copied manually, nothing is synced.
  let devTools: ReactNode = null;
  if (import.meta.env.DEV) {
    try {
      const { clearAgentationStorage } = await import("@/shared/dev/agentation-cleanup");
      clearAgentationStorage();
      const { AgentationDevToolbar } = await import("@/shared/dev/agentation-dev-toolbar");
      devTools = <AgentationDevToolbar />;
    } catch (error) {
      // A dev-only toolbar must never stop the app from rendering.
      console.warn("Agentation dev toolbar unavailable", error);
    }
  }
  createRoot(rootElement).render(
    <StrictMode>
      <AppProviders>
        <AppRouter />
      </AppProviders>
      {devTools}
    </StrictMode>,
  );
}

void bootstrap();
