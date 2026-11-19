import { useTranslation } from "react-i18next";
import { Moon, Sun } from "lucide-react";
import { Button } from "@/shared/components/ui/button";
import { useTheme } from "@/app/providers/theme-provider";

/**
 * Light/Dark toggle following the frozen template's Light-Dark control,
 * including the view-transition sweep. The sweep is skipped when the user
 * prefers reduced motion (yearning-ui-design-spec.md §12).
 */
export function ThemeToggle() {
  const { t } = useTranslation();
  const { resolvedTheme, setTheme } = useTheme();

  const toggleTheme = () => {
    const next = resolvedTheme === "light" ? "dark" : "light";
    const apply = () => {
      setTheme(next);
    };
    const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    if (reduceMotion || typeof document.startViewTransition !== "function") {
      apply();
      return;
    }
    const transition = document.startViewTransition(apply);
    void transition.ready.then(() => {
      document.documentElement.animate(
        {
          clipPath: ["inset(0 0 100% 0)", "inset(0)"],
        },
        {
          duration: 450,
          easing: "cubic-bezier(0.23, 1, 0.32, 1)",
          pseudoElement: "::view-transition-new(root)",
        },
      );
    });
  };

  return (
    <Button
      variant="ghost"
      size="icon"
      className="btn-circle-hover"
      aria-label={t("shell.toggleTheme")}
      onClick={toggleTheme}
    >
      {resolvedTheme === "light" ? <Moon className="size-5" /> : <Sun className="size-5" />}
    </Button>
  );
}
