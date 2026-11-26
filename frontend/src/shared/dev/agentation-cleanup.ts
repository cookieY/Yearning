import type { Annotation } from "agentation";

/**
 * Dev-only Agentation storage cleanup (RCP-20261103-FE-AGENTATION-DEV-TOOL).
 *
 * Agentation has no switch that disables persistence: every annotation —
 * including the selected and nearby text it captures automatically — is written
 * to localStorage and kept for 7 days. Dev annotations are meant to be
 * ephemeral, and SQL, evidence or credentials must not sit in browser storage
 * (frontend/CLAUDE.md), so the previous session's state is dropped at boot.
 *
 * This module exists only so the browser-storage scan can allowlist one small,
 * single-purpose dev file instead of the application entry point. It is loaded
 * through a dynamic import inside the `import.meta.env.DEV` branch, so it never
 * reaches a production build.
 */
const ANNOTATION_STORAGE_PREFIX = "feedback-annotations-";
const ANNOTATION_RETENTION_MS = 7 * 24 * 60 * 60 * 1000;

const AGENTATION_STORAGE_PREFIXES = [
  ANNOTATION_STORAGE_PREFIX,
  // Holds the toolbar settings, which can include a webhook URL that would POST
  // annotations out of the page; clear it so a stale webhook can never fire.
  "feedback-toolbar-settings",
  "agentation-design-",
  "agentation-rearrange-",
  "agentation-wireframe-",
  "agentation-session-",
] as const;

interface PageAnnotations {
  pathname: string;
  annotations: Annotation[];
}

/**
 * Removes the toolbar's annotation and settings keys. The pure UI preferences
 * (`feedback-toolbar-theme`, `feedback-toolbar-position`) are left alone: they
 * carry no page content. `feedback-freeze-styles` is a DOM style-element id, not
 * a storage key, so it is deliberately absent here.
 */
function clearAgentationStorage(): void {
  try {
    for (let index = localStorage.length - 1; index >= 0; index -= 1) {
      const key = localStorage.key(index);
      if (key === null) {
        continue;
      }
      if (AGENTATION_STORAGE_PREFIXES.some((prefix) => key.startsWith(prefix))) {
        localStorage.removeItem(key);
      }
    }
  } catch {
    // Storage can be unavailable (private mode); the toolbar still works in memory.
  }
}

/**
 * Reads all non-expired Agentation annotations across every pathname stored in
 * `localStorage` during the current dev session.
 */
function loadAllAgentationAnnotations(): PageAnnotations[] {
  const pages: PageAnnotations[] = [];
  const cutoff = Date.now() - ANNOTATION_RETENTION_MS;
  try {
    for (let index = 0; index < localStorage.length; index += 1) {
      const key = localStorage.key(index);
      if (key === null || !key.startsWith(ANNOTATION_STORAGE_PREFIX)) {
        continue;
      }
      const raw = localStorage.getItem(key);
      if (!raw) {
        continue;
      }
      let parsed: unknown;
      try {
        parsed = JSON.parse(raw);
      } catch {
        continue;
      }
      if (!Array.isArray(parsed)) {
        continue;
      }
      const valid = parsed.filter(
        (item): item is Annotation =>
          typeof item === "object" &&
          item !== null &&
          (typeof (item as Annotation).timestamp !== "number" ||
            (item as Annotation).timestamp > cutoff),
      );
      if (valid.length === 0) {
        continue;
      }
      const pathname = key.slice(ANNOTATION_STORAGE_PREFIX.length) || "/";
      pages.push({ pathname, annotations: valid });
    }
  } catch {
    return [];
  }
  pages.sort((a, b) => a.pathname.localeCompare(b.pathname));
  return pages;
}

/**
 * Formats annotations from multiple pages using Agentation's standard Markdown
 * format (`## Page Feedback: <pathname>` + `### <n>. <element>`).
 */
function formatAllAgentationMarkdown(pages: PageAnnotations[]): string {
  if (pages.length === 0) {
    return "";
  }
  const viewport =
    typeof window !== "undefined"
      ? `${String(window.innerWidth)}×${String(window.innerHeight)}`
      : "unknown";
  const lines: string[] = [];

  for (const page of pages) {
    lines.push(`## Page Feedback: ${page.pathname}`);
    lines.push(`**Viewport:** ${viewport}`);
    lines.push("");
    page.annotations.forEach((item, index) => {
      const title = item.element || item.elementPath || "Element";
      lines.push(`### ${String(index + 1)}. ${title}`);
      if (item.elementPath) {
        lines.push(`**Location:** ${item.elementPath}`);
      }
      if (item.sourceFile) {
        lines.push(`**Source:** ${item.sourceFile}`);
      }
      if (item.reactComponents) {
        lines.push(`**React:** ${item.reactComponents}`);
      }
      if (item.selectedText) {
        lines.push(`**Selected text:** "${item.selectedText}"`);
      }
      if (item.comment) {
        lines.push(`**Feedback:** ${item.comment}`);
      }
      lines.push("");
    });
  }

  return lines.join("\n").trimEnd();
}

export {
  clearAgentationStorage,
  formatAllAgentationMarkdown,
  loadAllAgentationAnnotations,
  type PageAnnotations,
};
