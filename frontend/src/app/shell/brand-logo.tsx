import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import logoOnLight from "@/assets/brand/logo-on-light.jpeg";
import logoOnDark from "@/assets/brand/logo-on-dark.png";
import { cn } from "@/shared/lib/utils";

/**
 * Yearning brand wordmark, following the frozen template's FullLogo pattern:
 * the light-surface variant renders in light mode and the dark-surface
 * variant in dark mode. Provenance and hashes live in
 * src/assets/brand/asset-manifest.json; the link carries the product name so
 * the logo image is never the sole carrier of the brand.
 */
export function BrandLogo({ className }: { className?: string }) {
  const { t } = useTranslation();
  return (
    <Link
      to="/workspace"
      aria-label={t("app.name")}
      className={cn(
        "group-data-[state=collapsed]:max-w-9 group-data-[state=collapsed]:w-9 block w-full max-w-[140px] overflow-hidden",
        className,
      )}
    >
      {/* h-auto + collapsed max-width keep the wordmark fully visible inside
          the narrow icon rail (owner ruling 2026-09-02): the asset scales
          proportionally instead of being clipped. */}
      {/* The legacy light-mode asset is a JPEG with a baked white background.
          Multiply makes that white transparent against the off-white sidebar
          (white × surface = surface); without it the logo renders as a white
          rectangle on every non-pure-white surface. */}
      <img
        src={logoOnLight}
        alt=""
        width={140}
        height={66}
        className="block h-auto w-full mix-blend-multiply group-data-[state=collapsed]:h-9 group-data-[state=collapsed]:w-auto group-data-[state=collapsed]:max-w-none dark:hidden"
      />
      <img
        src={logoOnDark}
        alt=""
        width={140}
        height={66}
        className="hidden h-auto w-full group-data-[state=collapsed]:h-9 group-data-[state=collapsed]:w-auto group-data-[state=collapsed]:max-w-none dark:block"
      />
    </Link>
  );
}
