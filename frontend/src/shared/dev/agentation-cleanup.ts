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
const AGENTATION_STORAGE_PREFIXES = [
  "feedback-annotations-",
  // Holds the toolbar settings, which can include a webhook URL that would POST
  // annotations out of the page; clear it so a stale webhook can never fire.
  "feedback-toolbar-settings",
  "agentation-design-",
  "agentation-rearrange-",
  "agentation-wireframe-",
  "agentation-session-",
] as const;

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

export { clearAgentationStorage };
