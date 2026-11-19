/**
 * RFC 4122 v4 UUID that also works outside secure contexts.
 *
 * `crypto.randomUUID()` only exists on HTTPS / localhost; the app is also
 * served over plain HTTP on a LAN address, where calling it throws before any
 * request is sent. `crypto.getRandomValues()` is available everywhere.
 */
export function randomUUID(): string {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  const hex = Array.from(bytes, (value, index) => {
    // Version 4 in byte 6, RFC 4122 variant in byte 8.
    const byte = index === 6 ? (value & 0x0f) | 0x40 : index === 8 ? (value & 0x3f) | 0x80 : value;
    return byte.toString(16).padStart(2, "0");
  });
  return `${hex.slice(0, 4).join("")}-${hex.slice(4, 6).join("")}-${hex.slice(6, 8).join("")}-${hex.slice(8, 10).join("")}-${hex.slice(10).join("")}`;
}
