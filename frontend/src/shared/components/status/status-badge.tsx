import type { ComponentProps } from "react";
import { Badge } from "@/shared/components/ui/badge";
import { STATUS_TONE_CLASS, type StatusTone } from "@/shared/lib/status-tone";
import { cn } from "@/shared/lib/utils";

/** The state label remains domain-owned; this composes the shared shadcn pill. */
export function StatusBadge({ tone = "stone", className, ...props }: Omit<ComponentProps<typeof Badge>, "variant"> & { tone?: StatusTone }) {
  return <Badge variant="outline" className={cn(STATUS_TONE_CLASS[tone], className)} {...props} />;
}
