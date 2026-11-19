/**
 * Pure TypeScript mirror of backend `promptprofile.Resolve`
 * (`backend/internal/review/promptprofile/profile.go`).
 *
 * Resolves the effective System Prompt & privacy track (`"default"` vs `"san"`)
 * and whether the resolution comes from an explicit configuration override or
 * deterministic `model_name` inference. Transitional `xmodel`/`san`/`sql-audit-nano`
 * `v1`/`v2`/`v3` checkpoints (e.g. `xmodel-v3.1`, `san-v3.1`, `SAN-4B-v3.1`,
 * `san:v3.1`, `sql-audit-nano:v3.1`) stay pinned on the anonymized `"default"`
 * track so vLLM/Ollama rollouts keep full SQL anonymization and legacy prompts.
 */

export type SystemPromptProfile = "default" | "san";
export type PromptProfileSetting = "" | SystemPromptProfile;
export type PromptProfileSource = "explicit" | "auto";

const LEGACY_ANONYMIZED_MODEL_RE =
  /^(?:xmodel|san|sql-audit-nano)(?:[-_.](?:2b|4b))?[-_.](?:v1|v2|v3)(?:$|[^a-z0-9])/;
const LEGACY_ANONYMIZED_TAG_RE =
  /^(?:(?:2b|4b)[-_.])?(?:v1|v2|v3)(?:$|[^a-z0-9])/;

export function resolvePromptProfile(
  modelName: string,
  explicit: PromptProfileSetting,
): { profile: SystemPromptProfile; source: PromptProfileSource } {
  if (explicit === "default" || explicit === "san") {
    return { profile: explicit, source: "explicit" };
  }

  const normalized = modelName.trim().toLowerCase();
  const slashIndex = normalized.lastIndexOf("/");
  const tail = slashIndex >= 0 ? normalized.slice(slashIndex + 1) : normalized;
  const colonIndex = tail.indexOf(":");
  const base = colonIndex >= 0 ? tail.slice(0, colonIndex) : tail;
  const tag = colonIndex >= 0 ? tail.slice(colonIndex + 1) : "";

  if (LEGACY_ANONYMIZED_MODEL_RE.test(base)) {
    return { profile: "default", source: "auto" };
  }

  const isSanFamily =
    base === "san" ||
    base === "sql-audit-nano" ||
    base.startsWith("san-") ||
    base.startsWith("san_") ||
    base.startsWith("san.") ||
    base.startsWith("sql-audit-nano-") ||
    base.startsWith("sql-audit-nano_") ||
    base.startsWith("sql-audit-nano.");
  const isLegacyXmodelFamily =
    base === "xmodel" ||
    base.startsWith("xmodel-") ||
    base.startsWith("xmodel_") ||
    base.startsWith("xmodel.");

  if (
    (isSanFamily || isLegacyXmodelFamily) &&
    tag !== "" &&
    LEGACY_ANONYMIZED_TAG_RE.test(tag)
  ) {
    return { profile: "default", source: "auto" };
  }
  if (isSanFamily) {
    return { profile: "san", source: "auto" };
  }
  return { profile: "default", source: "auto" };
}
