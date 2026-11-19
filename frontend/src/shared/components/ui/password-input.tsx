"use client";

import * as React from "react";
import { Eye, EyeOff } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
} from "@/shared/components/ui/input-group";
import { cn } from "@/shared/lib/utils";

type PasswordInputProps = Omit<React.ComponentProps<"input">, "type"> & {
  /** Optional label overrides for the reveal toggle (defaults to common.* plaintext keys). */
  revealLabel?: string;
  hideLabel?: string;
  /** Optional wrapper classes forwarded to InputGroup (e.g., height/radius overrides). */
  groupClassName?: string;
};

/**
 * Password input with a reveal toggle (owner ruling 2026-10-31: every
 * password-style input must expose its plaintext). Composed from the frozen
 * Shadcn Dashboard template's input-group primitives using the input-11
 * inline-end button pattern — the registry has no ready-made password block
 * (MCP search verified), so the sanctioned composition is InputGroup +
 * InputGroupInput + InputGroupAddon(inline-end) + InputGroupButton.
 */
function PasswordInput({ revealLabel, hideLabel, groupClassName, ...props }: PasswordInputProps) {
  const { t } = useTranslation();
  const [revealed, setRevealed] = React.useState(false);
  return (
    <InputGroup className={cn("w-full", groupClassName)}>
      <InputGroupInput
        type={revealed ? "text" : "password"}
        {...props}
      />
      <InputGroupAddon align="inline-end">
        <InputGroupButton
          size="icon-xs"
          aria-label={revealed ? (hideLabel ?? t("common.hidePlaintext")) : (revealLabel ?? t("common.showPlaintext"))}
          aria-pressed={revealed}
          onClick={() => { setRevealed((value) => !value); }}
        >
          {revealed ? <EyeOff /> : <Eye />}
        </InputGroupButton>
      </InputGroupAddon>
    </InputGroup>
  );
}

export { PasswordInput };
