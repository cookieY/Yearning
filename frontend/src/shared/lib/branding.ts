import { useSyncExternalStore } from "react";

export const DEFAULT_PRODUCT_NAME = "Yearning";

export interface PublicBranding {
  productName: string;
  loginDescription: string;
}

const DEFAULT_BRANDING: PublicBranding = {
  productName: DEFAULT_PRODUCT_NAME,
  loginDescription: "",
};

let activeBranding: PublicBranding = DEFAULT_BRANDING;
const listeners = new Set<() => void>();

function syncDocumentTitle(productName: string): void {
  if (typeof document !== "undefined") {
    document.title = productName;
  }
}

export function setPublicBranding(
  raw?: { product_name?: string | null; login_description?: string | null } | null,
): void {
  const productName = (raw?.product_name ?? "").trim() || DEFAULT_PRODUCT_NAME;
  const loginDescription = (raw?.login_description ?? "").trim();
  syncDocumentTitle(productName);
  if (
    activeBranding.productName === productName &&
    activeBranding.loginDescription === loginDescription
  ) {
    return;
  }
  activeBranding = { productName, loginDescription };
  for (const listener of listeners) {
    listener();
  }
}

export function resetPublicBranding(): void {
  syncDocumentTitle(DEFAULT_PRODUCT_NAME);
  if (
    activeBranding.productName === DEFAULT_BRANDING.productName &&
    activeBranding.loginDescription === DEFAULT_BRANDING.loginDescription
  ) {
    return;
  }
  activeBranding = DEFAULT_BRANDING;
  for (const listener of listeners) {
    listener();
  }
}

export function usePublicBranding(): PublicBranding {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    () => activeBranding,
    () => activeBranding,
  );
}
