import { motion, useReducedMotion } from "motion/react";
import { useTranslation } from "react-i18next";
import { Database } from "lucide-react";
import type { Flow } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/shared/components/ui/card";

/**
 * Review-flow timeline card (owner ruling 2026-10-31, reference: pipeline
 * timeline card — vertical node rail with connector lines, mono subtitles).
 * Lives under the Order Summary card on the submission wizard; the summary card
 * itself no longer repeats the flow row. MCP registry has no free timeline
 * block (only the PRO activity-timeline-card) and the frozen template ships
 * no stepper primitive, so this is composed from the sanctioned Card
 * primitive + lucide icons in the block visual vocabulary (pending node =
 * ring circle with muted dot, per badge-07/08/09 node language).
 *
 * Draft-time honesty: nothing has started before submission, so every stage
 * renders as pending — the card shows the frozen stage chain (position +
 * stage datasource name resolved server-side, RCP-20260904), never invented
 * progress.
 */
export function FlowTimelineCard({ flow }: { flow: Flow | null }) {
  const { t } = useTranslation();
  const stages = flow?.stages ?? [];
  const reduced = useReducedMotion();
  const timeline = (
    <Card className="[--card-spacing:--spacing(4)]" data-testid="wizard-flow-timeline">
      <CardHeader>
        <CardTitle className="truncate text-base font-semibold">
          {flow?.name ?? t("precheck.wizard.timeline.unselected")}
        </CardTitle>
        <CardDescription>{t("precheck.new.flowCardDescription")}</CardDescription>
      </CardHeader>
      <CardContent>
        {stages.length === 0 ? (
          <p className="text-muted-foreground text-sm">
            {t("precheck.wizard.timeline.unselected")}
          </p>
        ) : (
          <ol className="flex flex-col" data-testid="wizard-flow-timeline-steps">
            {stages.map((stage, index) => (
            <motion.li
              key={stage.position}
              initial={reduced ? false : { opacity: 0, x: -10 }}
              animate={{ opacity: 1, x: 0 }}
              transition={{ delay: index * 0.08, duration: 0.25, ease: "easeOut" }}
              className="flex gap-3">
                {/* Node column: ring node + inter-stage connector line. */}
                <div className="flex flex-col items-center">
                  <span
                    className="border-border bg-muted/40 flex size-7 shrink-0 items-center justify-center rounded-full border"
                    aria-hidden
                    data-testid={`wizard-flow-timeline-node-${String(stage.position)}`}
                  >
                    <span className="bg-muted-foreground/50 size-1.5 rounded-full" />
                  </span>
                  {index < stages.length - 1 && (
                    <span className="bg-border w-px flex-1 py-1" aria-hidden />
                  )}
                </div>
                <div className={index < stages.length - 1 ? "pb-5" : ""}>
                  <p className="text-foreground text-sm font-semibold">
                    {t("precheck.stagePath.stageDatasource", {
                      position: stage.position,
                      name: stage.datasource_name,
                    })}
                  </p>
                  <p className="text-muted-foreground mt-0.5 flex items-center gap-1.5 font-mono text-xs">
                    <Database className="size-3" aria-hidden />
                    {stage.datasource_name}
                  </p>
                </div>
            </motion.li>
            ))}
          </ol>
        )}
      </CardContent>
    </Card>
  );
  if (reduced) return timeline;
  return (
    <motion.div
      initial={{ opacity: 0, y: 10 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.16, ease: "easeOut" }}
    >
      {timeline}
    </motion.div>
  );
}
