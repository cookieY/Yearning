import type { ComponentProps } from "react";
import { Button } from "@/shared/components/ui/button";
import { Spinner } from "@/shared/components/ui/spinner";

/** Compose the stock Button and Spinner; retain its label and prevent duplicate actions. */
export function PendingButton({ pending, disabled, children, ...props }: ComponentProps<typeof Button> & { pending: boolean }) {
  return (
    <Button {...props} disabled={pending || disabled} aria-busy={pending || undefined}>
      {pending && <Spinner aria-hidden data-icon="inline-start" />}
      {children}
    </Button>
  );
}
