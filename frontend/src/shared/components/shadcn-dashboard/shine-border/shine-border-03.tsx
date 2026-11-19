"use client";

import { ArrowRight, BarChart3, Globe, Zap, type LucideIcon } from "lucide-react";

import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent, CardHeader } from "@/shared/components/ui/card";
import {
  ShineBorder,
  type ShineBorderProps,
} from "@/shared/components/ui/shine-border";
import { cn } from "@/shared/lib/utils";

/**
 * Shadcn Dashboard `shine-border-03` block (vendored 2026-11-11). Import paths
 * follow `components.json` aliases and palette utilities map to the semantic
 * `--shine-*` and `--tone-*` tokens defined in `src/app/styles/global.css`.
 */

export { ShineBorder, type ShineBorderProps };

type FeatureItem = {
  icon: LucideIcon;
  title: string;
  description: string;
  color: string;
  bg: string;
};

const features: FeatureItem[] = [
  {
    icon: Zap,
    title: "Blazing Fast",
    description: "Optimized for sub-100ms response times globally.",
    color: "text-tone-amber",
    bg: "bg-tone-amber/10",
  },
  {
    icon: BarChart3,
    title: "Real-time Analytics",
    description: "Track every event and conversion as it happens.",
    color: "text-tone-sky",
    bg: "bg-tone-sky/10",
  },
  {
    icon: Globe,
    title: "Edge Deployed",
    description: "Runs on 300+ PoPs across 6 continents worldwide.",
    color: "text-tone-teal",
    bg: "bg-tone-teal/10",
  },
];

export function FeatureCard() {
  return (
    <Card className="relative h-full rounded-2xl !border-0 bg-transparent !shadow-none !ring-0">
      <CardHeader className="p-6 pb-2">
        <Badge className="mb-2 w-fit" variant="secondary">
          Platform Features
        </Badge>
        <h3 className="text-foreground text-xl leading-snug font-semibold">
          Everything you need to{" "}
          <span className="text-primary">ship faster</span>
        </h3>
        <p className="text-muted-foreground mt-1 text-sm">
          Hover to explore what makes our platform stand out.
        </p>
      </CardHeader>

      <CardContent className="flex flex-col gap-4 p-6 pt-4">
        {features.map((f) => (
          <div
            key={f.title}
            className="hover:bg-muted/60 group flex items-start gap-3 rounded-xl p-3 transition-colors"
          >
            <div
              className={cn(
                "flex size-9 shrink-0 items-center justify-center rounded-lg",
                f.bg,
              )}
            >
              <f.icon className={cn("size-4", f.color)} />
            </div>
            <div>
              <p className="text-foreground text-sm font-semibold">{f.title}</p>
              <p className="text-muted-foreground mt-0.5 text-xs">
                {f.description}
              </p>
            </div>
          </div>
        ))}

        <Button className="mt-2 h-10 w-full gap-2">
          Explore All Features
          <ArrowRight className="size-4" />
        </Button>
      </CardContent>
    </Card>
  );
}

export default function ShineBorderDemo() {
  return (
    <ShineBorder
      borderWidth={2}
      gradient="from-shine-from via-shine-via to-shine-to"
      className="w-full max-w-sm"
    >
      <FeatureCard />
    </ShineBorder>
  );
}
