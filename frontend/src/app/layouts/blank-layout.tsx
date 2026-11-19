import type { ReactNode } from "react";

/**
 * Structure baseline of the pre-login layout, mirroring the frozen Shadcn
 * Dashboard BlankLayout; the real branded login experience is FE-F2 scope.
 */
export function BlankLayout({ children }: { children: ReactNode }) {
  return <div className="min-h-screen bg-background text-foreground">{children}</div>;
}
