// Pure validation mirrors for admin mock fixtures, retiring MOCK-LENIENT-01..03.

const REVIEW_DEF_KEYS = new Set(["knowledge_text", "max_severity", "version"]);
const PEM_BASE64_RE = /^[A-Za-z0-9+/]+={0,2}$/;

export function validateTLSWrite(tls: unknown): string | null {
  if (tls === null || tls === undefined) return null;
  if (typeof tls !== "object" || Array.isArray(tls)) return "tls must be an object or null";
  const block = tls as {
    ca_pem?: { value: string } | null;
    client_cert_pem?: { value: string } | null;
    client_key_pem?: { value: string } | null;
  };
  const ca = block.ca_pem?.value ?? "";
  const cert = block.client_cert_pem?.value ?? "";
  const key = block.client_key_pem?.value ?? "";
  if (ca === "" && cert === "" && key === "") return "tls block requires at least one material";
  if ((cert === "") !== (key === "")) return "client certificate and key must be supplied together";

  const materials: Array<[string, string, boolean]> = [
    ["ca_pem", ca, true],
    ["client_cert_pem", cert, true],
    ["client_key_pem", key, false],
  ];
  const blockRe = /-----BEGIN ([A-Z0-9 ]+)-----([\s\S]*?)-----END \1-----/g;
  for (const [name, material, wantCert] of materials) {
    if (material === "") continue;
    let count = 0;
    for (const match of material.matchAll(blockRe)) {
      count += 1;
      const blockType = match[1] ?? "";
      if ((blockType === "CERTIFICATE") !== wantCert) {
        return `${name} blocks must be ${wantCert ? "CERTIFICATE" : "private-key"} PEM`;
      }
      const cleaned = (match[2] ?? "").replace(/\s+/g, "");
      if (cleaned.length < 2 || cleaned.length % 4 === 1 || !PEM_BASE64_RE.test(cleaned)) {
        return `${name} contains invalid base64 PEM payload`;
      }
      try {
        if (atob(cleaned).length === 0) return `${name} PEM block is empty`;
      } catch {
        return `${name} contains invalid base64 PEM payload`;
      }
    }
    if (count === 0) return `${name} must be PEM-encoded`;
  }
  return null;
}

interface CredentialLike {
  purpose: string;
  username?: string | null;
  password?: { value?: string } | null;
  reuse_credential_purpose?: string | null;
}

export function validateDatasourceWrite(
  body: Record<string, unknown>,
  purposes: readonly string[],
  engineModeOK: (engine: string, mode: string) => boolean,
): string | null {
  const name = typeof body.name === "string" ? body.name.trim() : "";
  const host = typeof body.host === "string" ? body.host.trim() : "";
  if (name === "" || name.length > 128 || host === "" || host.length > 253 || /[\s/?#@]/.test(host)) {
    return "name and host are required and host must be valid";
  }
  const port = Number(body.port ?? 0);
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    return "port must be valid";
  }
  if (body.version_constraint !== undefined && body.version_constraint !== null) {
    if (typeof body.version_constraint !== "string" || body.version_constraint.length > 64) {
      return "version_constraint must be valid";
    }
    const vc = body.version_constraint.trim();
    if (vc !== "" && !/^[A-Za-z0-9._,<>=~^*+-]+$/.test(vc)) {
      return "version_constraint must match version format";
    }
  }
  if (body.deployment_kind !== "native" && body.deployment_kind !== "cloud") {
    return "deployment_kind is invalid";
  }
  const engine = typeof body.engine === "string" ? body.engine : "";
  const compatibilityMode = typeof body.compatibility_mode === "string" ? body.compatibility_mode : "";
  if (!engineModeOK(engine, compatibilityMode)) {
    return "engine and compatibility_mode do not match the supported matrix";
  }
  const credentials = Array.isArray(body.credentials) ? (body.credentials as CredentialLike[]) : [];
  if (credentials.length < 1 || credentials.length > 3) {
    return "at least one and at most three credentials are required";
  }
  const seen = new Set<string>();
  for (const credential of credentials) {
    if (!purposes.includes(credential.purpose)) return "credential purpose is invalid";
    if (seen.has(credential.purpose)) return "credential purposes must be unique";
    seen.add(credential.purpose);
    const hasPassword = credential.password != null && (credential.password.value ?? "") !== "";
    const reuse = credential.reuse_credential_purpose;
    if (hasPassword && reuse != null) return "password and reuse_credential_purpose are mutually exclusive";
    if (!hasPassword && (reuse == null || reuse === "")) {
      return "credential requires exactly one of password or reuse_credential_purpose";
    }
    if (!hasPassword && reuse === credential.purpose) {
      if (credential.username != null && credential.username !== "") {
        return "username must be absent in keep mode";
      }
      continue;
    }
    if (typeof credential.username !== "string" || credential.username.trim() === "") {
      return "credential username is required";
    }
  }
  const tlsError = validateTLSWrite("tls" in body ? body.tls : null);
  if (tlsError !== null) return tlsError;
  if (body.test_purpose !== undefined && (typeof body.test_purpose !== "string" || !purposes.includes(body.test_purpose))) {
    return "test_purpose is invalid";
  }
  return null;
}

export function validateReviewInputDefinition(definition: unknown): string | null {
  if (typeof definition !== "object" || definition === null || Array.isArray(definition)) {
    return "definition is required";
  }
  const def = definition as Record<string, unknown>;
  for (const key of Object.keys(def)) {
    if (!REVIEW_DEF_KEYS.has(key)) return `definition.${key} is not supported`;
  }
  if (typeof def.knowledge_text !== "string" || def.knowledge_text.trim().length === 0) {
    return "knowledge_text is required";
  }
  if (def.knowledge_text.length > 65536) {
    return "knowledge_text exceeds maximum length";
  }
  if (typeof def.max_severity !== "string" || !["info", "low", "medium", "high", "critical"].includes(def.max_severity)) {
    return "max_severity must be one of info/low/medium/high/critical";
  }
  if (typeof def.version !== "number" || !Number.isInteger(def.version) || def.version < 1) {
    return "definition.version must be a positive integer";
  }
  return null;
}

export function validateKnowledgeDefinition(definition: unknown): string | null {
  const baseErr = validateReviewInputDefinition(definition);
  if (baseErr !== null) return baseErr;
  const def = definition as Record<string, unknown>;
  if (typeof def.max_severity !== "string" || !["info", "low", "medium"].includes(def.max_severity)) {
    return "max_severity must be one of info/low/medium";
  }
  return null;
}
