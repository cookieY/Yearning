/**
 * Release notes shaping for the home release banner. Since
 * RCP-20261020-RELEASE-FEED-BACKEND the payload arrives as the backend's
 * ReleaseSnapshot (upstream GitHub release metadata fetched server-side);
 * this module only owns the plain-text excerpt.
 */

/** Release notes are free markdown; the banner shows one plain-text line,
 * hard-truncated at 50 characters with an ellipsis (owner ruling). */
export function summarizeReleaseNotes(body: string, limit = 50): string {
  const plain = body
    .replace(/<!--[\s\S]*?-->/g, " ")
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/`([^`]*)`/g, "$1")
    .replace(/!\[[^\]]*\]\([^)]*\)/g, " ")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/^\s*#{1,6}\s+/gm, "")
    .replace(/^\s*[-*+]\s+/gm, "")
    .replace(/^\s*\d+\.\s+/gm, "")
    .replace(/[*_~>]/g, "")
    .replace(/\s+/g, " ")
    .trim();
  return plain.length > limit ? `${plain.slice(0, limit)}…` : plain;
}
