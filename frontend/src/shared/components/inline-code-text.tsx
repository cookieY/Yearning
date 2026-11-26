export type BacktickSegment = { kind: "text" | "code"; value: string };

/** Split on complete backtick pairs only; an odd trailing tick stays literal. */
export function splitBacktickSegments(input: string): BacktickSegment[] {
  const segments: BacktickSegment[] = [];
  let rest = input;
  for (;;) {
    const start = rest.indexOf("`");
    if (start === -1) break;
    const end = rest.indexOf("`", start + 1);
    if (end === -1) break;
    if (start > 0) segments.push({ kind: "text", value: rest.slice(0, start) });
    segments.push({ kind: "code", value: rest.slice(start + 1, end) });
    rest = rest.slice(end + 1);
  }
  if (rest.length > 0) segments.push({ kind: "text", value: rest });
  return segments;
}

/**
 * Safely renders prose containing paired backticks as inline <code> elements
 * with zero HTML injection risk (no dangerouslySetInnerHTML).
 */
export function InlineCodeText({
  text,
  codeClassName = "bg-muted text-foreground rounded px-1 py-0.5 font-mono text-[0.875em] [overflow-wrap:anywhere]",
}: {
  text: string;
  codeClassName?: string;
}) {
  const segments = splitBacktickSegments(text);
  return (
    <>
      {segments.map((segment, index) =>
        segment.kind === "code" ? (
          <code key={index} className={codeClassName} data-testid="inline-code">
            {segment.value}
          </code>
        ) : (
          <span key={index}>{segment.value}</span>
        ),
      )}
    </>
  );
}
