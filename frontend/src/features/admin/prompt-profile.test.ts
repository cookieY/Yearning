import { describe, expect, it } from "vitest";
import { resolvePromptProfile } from "@/features/admin/prompt-profile";

describe("resolvePromptProfile (frontend mirror of backend promptprofile.Resolve)", () => {
  it("prioritizes explicit overrides over model_name auto-detection", () => {
    expect(resolvePromptProfile("SAN-4B", "default")).toEqual({
      profile: "default",
      source: "explicit",
    });
    expect(resolvePromptProfile("deepseek-chat", "san")).toEqual({
      profile: "san",
      source: "explicit",
    });
    expect(resolvePromptProfile("", "san")).toEqual({
      profile: "san",
      source: "explicit",
    });
    expect(resolvePromptProfile("san-v3.1", "san")).toEqual({
      profile: "san",
      source: "explicit",
    });
  });

  it("pins legacy v1/v2/v3 xmodel, san, and sql-audit-nano checkpoints onto default", () => {
    const legacyAnonymizedModels = [
      "xmodel-v3.1",
      "san-v3.1",
      "SAN-4B-v3.1",
      "san-2b-v3.1",
      "sql-audit-nano-v3.1",
      "SQL-Audit-Nano-4B-v3.1",
      "local/xmodel_v3_1",
      "ollama/san-v3:latest",
      "vllm/xmodel.v3.1:fp16",
      "xmodel-v2",
      "xmodel-v1",
      "san:v3.1",
      "ollama/san:v3.1",
      "sql-audit-nano:v3.1",
      "sql-audit-nano:4b-v3.1",
      "ollama/xmodel:v3.1",
      "  SAN:2b_v2.0  ",
      "registry.local:5000/org/san:v3.1",
      "san:v3.1:fp16",
    ];
    for (const model of legacyAnonymizedModels) {
      expect(resolvePromptProfile(model, ""), model).toEqual({
        profile: "default",
        source: "auto",
      });
    }
  });

  it("routes versionless and v4/v30+ san and sql-audit-nano family models to san", () => {
    const sanModels = [
      "san",
      "SAN-2B",
      "SAN-4B",
      "SQL-Audit-Nano-4B",
      "sql-audit-nano:4b",
      "ollama/san:latest",
      "vllm/san_q4:fp16",
      "  SAN.4B  ",
      "registry.internal/team/sql-audit-nano-2b:fp16",
      "san-v4",
      "sql-audit-nano-v4",
      "san-v30",
      "registry.local:5000/org/san:4b",
    ];
    for (const model of sanModels) {
      expect(resolvePromptProfile(model, ""), model).toEqual({
        profile: "san",
        source: "auto",
      });
    }
  });

  it("defaults all commercial, unknown, or legacy base models to default", () => {
    const generalModels = [
      "",
      "   ",
      "deepseek-chat",
      "qwen3.5-4b",
      "my-san-proxy",
      "sandbox",
      "xmodel",
      "xmodel-v4",
      "gpt-4o",
    ];
    for (const model of generalModels) {
      expect(resolvePromptProfile(model, ""), model).toEqual({
        profile: "default",
        source: "auto",
      });
    }
  });
});
