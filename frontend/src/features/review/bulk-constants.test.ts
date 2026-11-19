import { describe, expect, it } from "vitest";
import {
  BULK_MODE_MIN_BYTES,
  BULK_MODE_MIN_STATEMENTS,
  FINGERPRINT_COMPLEXITY_WARNING_UNIQUE,
  FINGERPRINT_MAX_INPUT_BYTES,
  FINGERPRINT_MAX_STATEMENTS,
  FINGERPRINT_MAX_STATEMENT_BYTES,
  FINGERPRINT_MAX_UNIQUE,
} from "@/features/review/bulk-constants";

/**
 * Asserts the frozen mirror of api/contracts/sql-fingerprint.json (grouping
 * block). If an approved Requirement change updates the contract, these
 * assertions fail until the mirror is updated in the same change.
 */
describe("bulk-constants contract mirror", () => {
  it("matches the frozen sql-fingerprint grouping limits", () => {
    expect(FINGERPRINT_MAX_INPUT_BYTES).toBe(33554432);
    expect(FINGERPRINT_MAX_STATEMENT_BYTES).toBe(524288);
    expect(FINGERPRINT_MAX_STATEMENTS).toBe(100000);
    expect(FINGERPRINT_COMPLEXITY_WARNING_UNIQUE).toBe(200);
    expect(FINGERPRINT_MAX_UNIQUE).toBe(1000);
  });

  it("keeps bulk-mode thresholds aligned with the contract", () => {
    expect(BULK_MODE_MIN_BYTES).toBe(FINGERPRINT_MAX_STATEMENT_BYTES);
    expect(BULK_MODE_MIN_STATEMENTS).toBe(1000);
  });
});
