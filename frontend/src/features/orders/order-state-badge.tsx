import type { LucideIcon } from "lucide-react";
import {
  BellRing,
  CircleCheck,
  Clock,
  Hourglass,
  Info,
  Loader2,
  OctagonAlert,
  TriangleAlert,
} from "lucide-react";
import type { ChangeOrderState, StageState } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useTranslation } from "react-i18next";
import { Badge } from "@/shared/components/ui/badge";
import { cn } from "@/shared/lib/utils";
import {
  ORDER_STATE_TONE_CLASS,
  orderStateTone,
  stageStateTone,
  type OrderStateTone,
} from "./order-state";

/**
 * Aggregate/step state chip for order surfaces. Color is presentation only —
 * the state string itself stays the single source of truth (workorder PRD §4:
 * aggregate state is for list display only).
 *
 * owner ruling 2026-11-04 (Sonner 08 adoption): order state badges adopt the shadcn-dashboard
 * sonner-08 block's tone visual (src/shared/components/shadcn-dashboard/sonner/) —
 * icon placed inside a tinted circle (/15 fill), badge body uses 12% family fill + 30% border (STATUS_TONE_CLASS),
 * and the icon follows the badge text family color. Tones map 1:1 to icons following the block's icon semantics:
 *   CircleCheck completed · Clock scheduled · Info submitted · Loader2 running (spinning)
 *   BellRing pending approval/paperwork · Hourglass waiting for execution · TriangleAlert outcome unknown ·
 *   OctagonAlert failed/adverse outcome
 * From 2026-11-04 the waiting family (including "waiting for execution") uniformly uses Hourglass, reserving TriangleAlert
 * for partial/unknown outcomes.
 * This supersedes two earlier rulings (2026-10-31 three-family animated badges and 2026-11-04 flat approval-queue badges);
 * the approval queue consumes this same component so there is only one order-state definition across the repo.
 */

/** Sonner-08 tone grammar: one icon per order tone (see ruling above). */
const TONE_ICON: Record<OrderStateTone, LucideIcon> = {
  emerald: CircleCheck,
  teal: Clock,
  sky: Info,
  indigo: Loader2,
  violet: BellRing,
  amber: TriangleAlert,
  orange: TriangleAlert,
  waiting: Hourglass,
  rose: OctagonAlert,
  red: OctagonAlert,
  stone: BellRing,
};

/** Tinted circle behind the icon (the block's `badge` layer, /15 tint). */
const TONE_ICON_CIRCLE: Record<OrderStateTone, string> = {
  emerald: "bg-tone-emerald/15",
  teal: "bg-tone-teal/15",
  sky: "bg-tone-sky/15",
  indigo: "bg-tone-indigo/15",
  violet: "bg-tone-violet/15",
  amber: "bg-tone-amber/15",
  orange: "bg-tone-orange/15",
  waiting: "bg-tone-waiting/15",
  rose: "bg-tone-rose/15",
  red: "bg-tone-red/15",
  stone: "bg-foreground/10",
};

/** The sonner-08 tone chip shared by every order-surface state badge. */
export function StateToneChip({
  tone,
  label,
  className,
  ...props
}: Omit<React.ComponentProps<typeof Badge>, "children"> & {
  tone: OrderStateTone;
  label: string;
}) {
  const Icon = TONE_ICON[tone];
  return (
    <Badge
      variant="outline"
      className={cn(ORDER_STATE_TONE_CLASS[tone], className)}
      {...props}
    >
      <span
        className={cn(
          "flex size-4 shrink-0 items-center justify-center rounded-full",
          TONE_ICON_CIRCLE[tone],
        )}
        aria-hidden="true"
      >
        <Icon
          className={cn(
            "size-2.5",
            tone === "indigo" && "animate-spin motion-reduce:animate-none",
          )}
        />
      </span>
      {label}
    </Badge>
  );
}

export function OrderStateBadge({
  state,
  className,
  ...props
}: Omit<React.ComponentProps<typeof Badge>, "children"> & {
  state: ChangeOrderState;
}) {
  const { t } = useTranslation();
  return (
    <StateToneChip
      tone={orderStateTone(state)}
      label={t(`orders.state.${state}`)}
      className={className}
      {...props}
    />
  );
}

export function StageStateBadge({ state }: { state: StageState }) {
  const { t } = useTranslation();
  return <StateToneChip tone={stageStateTone(state)} label={t(`orders.stageState.${state}`)} />;
}
