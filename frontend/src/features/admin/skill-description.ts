import type { PromptToolWriteParameters } from "@/api/generated/client/yearningV4HTTPAPI.schemas";

const PARAM_PLACEHOLDER = /\{\{([a-z][a-z0-9_]*)\}\}/g;

/**
 * §18.80 addendum 1 (owner ruling): {{param}} placeholders in skill descriptions render dynamically
 * from current inline parameter values — table and rule-set combination list descriptions follow immediately
 * after parameter tuning, decoupled from static seeds. Missing parameters (user-authored descriptions referencing
 * non-existent keys) keep their placeholder verbatim.
 */
export function renderSkillDescription(
  description: string,
  parameters: PromptToolWriteParameters,
): string {
  return description.replace(PARAM_PLACEHOLDER, (raw, key: string) => {
    const value = parameters[key];
    if (value === undefined) return raw;
    return Array.isArray(value) ? value.join(", ") : String(value);
  });
}
