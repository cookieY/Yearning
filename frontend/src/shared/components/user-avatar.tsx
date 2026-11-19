import { cn } from "@/shared/lib/utils";

/**
 * Deterministic user avatar (shadcn-dashboard avatar-13 block, owner ruling
 * 2026-10-31): the same name always maps to the same color — a five-color
 * palette keyed by a 31-multiplier string hash, with two-letter initials
 * (first two chars of a single word, first+last of multiple words; CJK
 * names keep their first two characters).
 */

const PALETTE = [
  "avatar-swatch-orange",
  "avatar-swatch-amber",
  "avatar-swatch-teal",
  "avatar-swatch-blue",
  "avatar-swatch-sky",
];

export function avatarColorFor(name: string): string {
  let hash = 0;
  for (const char of name) hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  return PALETTE[hash % PALETTE.length] ?? "avatar-swatch-teal";
}

export function avatarInitialsOf(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  if (parts.length === 1) return (parts[0] ?? "").slice(0, 2).toUpperCase();
  const first = parts[0] ?? "";
  const last = parts[parts.length - 1] ?? "";
  return (first.charAt(0) + last.charAt(0)).toUpperCase();
}

export function UserAvatar({ name, className }: { name: string; className?: string }) {
  return (
    <span
      aria-hidden
      className={cn(
        "flex h-8 w-8 items-center justify-center rounded-full text-xs font-medium select-none",
        avatarColorFor(name),
        className,
      )}
    >
      {avatarInitialsOf(name)}
    </span>
  );
}
