import { useTranslation } from "react-i18next";
import { Search, X } from "lucide-react";
import { useRef } from "react";
import { InputGroup, InputGroupAddon, InputGroupButton, InputGroupInput } from "@/shared/components/ui/input-group";

/**
 * Shared table search box (owner request 2026-09-04, §18.37): filters the
 * table rows client-side by name substring. Renders with a leading search
 * icon and a clear affordance once text is entered.
 */
export function TableSearchInput({
  value,
  onChange,
  placeholder,
  testId,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  testId: string;
}) {
  const { t } = useTranslation();
  const inputRef = useRef<HTMLInputElement>(null);
  return (
    <InputGroup className="w-full min-w-0 max-w-xs">
      <InputGroupAddon><Search aria-hidden /></InputGroupAddon>
      <InputGroupInput
        ref={inputRef}
        aria-label={placeholder ?? t("table.searchPlaceholder")}
        value={value}
        onChange={(event) => { onChange(event.target.value); }}
        placeholder={placeholder ?? t("table.searchPlaceholder")}
        data-testid={testId}
      />
      {value !== "" && (
        <InputGroupAddon align="inline-end"><InputGroupButton
          type="button"
          aria-label={t("common.clear")}
          onClick={() => { onChange(""); inputRef.current?.focus(); }}
          size="icon-xs"
          data-testid={`${testId}-clear`}
        >
          <X aria-hidden />
        </InputGroupButton></InputGroupAddon>
      )}
    </InputGroup>
  );
}
