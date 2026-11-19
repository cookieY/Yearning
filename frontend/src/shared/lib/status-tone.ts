/** Theme-aware status surfaces shared by table and detail badges. */
export type StatusTone =
  | "emerald"
  | "teal"
  | "sky"
  | "indigo"
  | "violet"
  | "amber"
  | "orange"
  | "waiting"
  | "rose"
  | "red"
  | "stone";

export const STATUS_TONE_CLASS: Record<StatusTone, string> = {
  emerald: "border-tone-emerald/30 bg-tone-emerald/12 text-tone-emerald",
  teal: "border-tone-teal/30 bg-tone-teal/12 text-tone-teal",
  sky: "border-tone-sky/30 bg-tone-sky/12 text-tone-sky",
  indigo: "border-tone-indigo/30 bg-tone-indigo/12 text-tone-indigo",
  violet: "border-tone-violet/30 bg-tone-violet/12 text-tone-violet",
  amber: "border-tone-amber/30 bg-tone-amber/12 text-tone-amber",
  orange: "border-tone-orange/30 bg-tone-orange/12 text-tone-orange",
  waiting: "border-tone-waiting/30 bg-tone-waiting/12 text-tone-waiting",
  rose: "border-tone-rose/30 bg-tone-rose/12 text-tone-rose",
  red: "border-tone-red/30 bg-tone-red/12 text-tone-red",
  stone: "border-border bg-muted text-muted-foreground",
};
