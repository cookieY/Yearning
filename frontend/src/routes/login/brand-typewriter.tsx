import { useEffect, useState } from "react";

/**
 * Typewriter highlight for the login brand headline (owner ruling 2026-10-24,
 * per the shadcn animated-text-03 block): types the phrase out character by
 * character, holds, deletes, and retypes in an endless loop.
 *
 * Determinism contract shared with the rest of the app: under
 * prefers-reduced-motion the animation "completes instantly" — the component
 * mounts with the full phrase and never schedules a timer. The screenshot
 * harness captures with reducedMotion: reduce, so baselines always see the
 * settled phrase instead of racing whatever character the loop reached.
 */

const TYPE_INTERVAL_MS = 100;
const HOLD_FULL_PHRASE_MS = 1500;
const DELETE_INTERVAL_MS = 60;
const RETYPE_PAUSE_MS = 400;

type TypewriterPhase = "typing" | "deleting" | "idle";

interface TypewriterState {
  count: number;
  phase: TypewriterPhase;
}

function prefersReducedMotion(): boolean {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

interface BrandTypewriterProps {
  readonly phrase: string;
}

function schedule(delayMs: number, next: () => void): () => void {
  const id = setTimeout(next, delayMs);
  return () => {
    clearTimeout(id);
  };
}

export function BrandTypewriter({ phrase }: BrandTypewriterProps) {
  const [reduced] = useState(prefersReducedMotion);
  const [state, setState] = useState<TypewriterState>(() =>
    reduced ? { count: phrase.length, phase: "idle" } : { count: 0, phase: "typing" },
  );

  useEffect(() => {
    if (state.phase === "idle") return undefined;

    if (state.phase === "typing") {
      if (state.count < phrase.length) {
        return schedule(TYPE_INTERVAL_MS, () => {
          setState({ count: state.count + 1, phase: "typing" });
        });
      }
      return schedule(HOLD_FULL_PHRASE_MS, () => {
        setState({ count: state.count, phase: "deleting" });
      });
    }

    if (state.count > 0) {
      return schedule(DELETE_INTERVAL_MS, () => {
        setState({ count: state.count - 1, phase: "deleting" });
      });
    }
    return schedule(RETYPE_PAUSE_MS, () => {
      setState({ count: 0, phase: "typing" });
    });
  }, [state, phrase]);

  return <span>{phrase.slice(0, state.count)}</span>;
}
