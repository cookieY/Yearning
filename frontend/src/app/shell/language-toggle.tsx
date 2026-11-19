import { useTranslation } from "react-i18next";
import { Check, ChevronDown, Languages } from "lucide-react";
import { Button } from "@/shared/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/components/ui/dropdown-menu";
import { LOCALES, setLocale, type Locale } from "@/shared/i18n";

/** Native locale names — a locale list is not translated into itself. */
const LOCALE_LABELS: Record<Locale, string> = {
  "zh-CN": "\u7b80\u4f53\u4e2d\u6587",
  "en-US": "English",
};

/** Locale picker beside the theme toggle (owner ruling 2026-09-02): a
 * dropdown rather than a flip toggle — more locales are expected later. The
 * choice persists via the i18n module (storage allowlist owner). */
export function LanguageToggle() {
  const { t, i18n } = useTranslation();
  const current = (LOCALES as readonly string[]).includes(i18n.language)
    ? (i18n.language as Locale)
    : "zh-CN";
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        data-testid="locale-toggle"
        render={
          <Button
            variant="ghost"
            size="icon"
            className="btn-circle-hover w-auto gap-1 px-2"
            aria-label={t("shell.toggleLocale")}
          >
            <Languages className="size-5" aria-hidden />
            <ChevronDown className="size-3" aria-hidden />
          </Button>
        }
      />
      <DropdownMenuContent align="end" data-testid="locale-menu">
        {LOCALES.map((locale) => (
          <DropdownMenuItem
            key={locale}
            data-testid={`locale-option-${locale}`}
            onClick={() => {
              setLocale(locale);
            }}
          >
            {LOCALE_LABELS[locale]}
            {locale === current && <Check className="ml-auto size-4" aria-hidden />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
