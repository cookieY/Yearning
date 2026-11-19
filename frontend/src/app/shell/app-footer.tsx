import { useTranslation } from "react-i18next";

/**
 * The single global footer of the logged-in shell. The exact string is fixed
 * by docs/contracts/frontend-ui-migration-contract.md §1 and is license text,
 * so it is intentionally not translated. The sponsor/docs/source links on the
 * right (including the AGPL-3.0 section 13 source repository link) are inlined
 * from vite.config.ts defines; they render as plain muted text — no underline,
 * no hover color change.
 */
export const FOOTER_TEXT = "AGPL-3.0 Licensed | Copyright © 2017-present Henry Yee";

export function AppFooter() {
  const { t } = useTranslation();
  const linkClass =
    "text-sm text-muted-foreground no-underline hover:no-underline hover:text-muted-foreground "
    + "visited:text-muted-foreground focus-visible:text-muted-foreground transition-none";
  return (
    <footer className="pt-6 md:pr-36">
      <div className="flex flex-col items-center justify-between gap-3 text-center md:flex-row">
        <p className="text-sm text-muted-foreground">{FOOTER_TEXT}</p>
        <div className="flex items-center gap-4">
          <a href={__SOURCE_URL__} target="_blank" rel="noreferrer noopener" className={linkClass}>
            {t("shell.footer.source")}
          </a>
          <a href={__SPONSOR_URL__} target="_blank" rel="noreferrer noopener" className={linkClass}>
            {t("shell.footer.sponsor")}
          </a>
          <a href={__DOCS_URL__} target="_blank" rel="noreferrer noopener" className={linkClass}>
            {t("shell.footer.docs")}
          </a>
        </div>
      </div>
    </footer>
  );
}
