import type { ReactNode } from "react";
import { Link } from "react-router";
import { ROUTE_ALIAS_MAP } from "@/shared/lib/route-aliases";
import {
  CodeBlock,
  CodeBlockActions,
  CodeBlockContent,
  CodeBlockCopyButton,
  CodeBlockHeader,
  CodeBlockTitle,
} from "@/shared/components/ui/code-block";

export interface AssistantMarkdownProps {
  content: string;
  className?: string;
  isStreaming?: boolean;
}

/**
 * Resolves contract/help route aliases to canonical frontend router paths.
 */
export function resolveAssistantRoutePath(routePath: string): string {
  const trimmed = routePath.trim();
  const mapped = ROUTE_ALIAS_MAP[trimmed];
  if (mapped !== undefined) {
    return mapped;
  }
  if (trimmed.startsWith("/orders/") && trimmed !== "/orders/new") {
    return `/changes${trimmed}`;
  }
  return trimmed;
}

type BlockNode =
  | { type: "heading"; level: 1 | 2 | 3 | 4; text: string }
  | { type: "code"; language: string; code: string }
  | { type: "blockquote"; lines: string[] }
  | { type: "ul"; items: ListItemNode[] }
  | { type: "ol"; items: ListItemNode[] }
  | { type: "table"; headers: string[]; rows: string[][] }
  | { type: "hr" }
  | { type: "paragraph"; text: string };

interface ListItemNode {
  text: string;
  children: string[];
}

function isTableSeparator(line: string): boolean {
  const trimmed = line.trim();
  if (!trimmed.includes("|")) return false;
  const cells = trimmed
    .replace(/^\|/, "")
    .replace(/\|$/, "")
    .split("|")
    .map((cell) => cell.trim());
  return (
    cells.length > 0 &&
    cells.every((cell) => /^:?-{3,}:?$/.test(cell))
  );
}

function splitTableRow(line: string): string[] {
  return line
    .trim()
    .replace(/^\|/, "")
    .replace(/\|$/, "")
    .split("|")
    .map((cell) => cell.trim());
}

function parseBlocks(markdown: string): BlockNode[] {
  const normalized = markdown.replace(/\r\n/g, "\n");
  const lines = normalized.split("\n");
  const blocks: BlockNode[] = [];
  let i = 0;

  while (i < lines.length) {
    const rawLine = lines[i] ?? "";
    const trimmed = rawLine.trim();

    if (trimmed === "") {
      i += 1;
      continue;
    }

    // Fenced code block: ```lang
    if (trimmed.startsWith("```")) {
      const language = trimmed.slice(3).trim();
      const codeLines: string[] = [];
      i += 1;
      while (i < lines.length && !(lines[i] ?? "").trim().startsWith("```")) {
        codeLines.push(lines[i] ?? "");
        i += 1;
      }
      if (i < lines.length) {
        i += 1; // skip closing ```
      }
      blocks.push({
        type: "code",
        language,
        code: codeLines.join("\n"),
      });
      continue;
    }

    // Horizontal rule
    if (/^(-{3,}|\*{3,}|_{3,})$/.test(trimmed)) {
      blocks.push({ type: "hr" });
      i += 1;
      continue;
    }

    // Heading (# to ####)
    const headingMatch = /^(#{1,4})\s+(.+)$/.exec(trimmed);
    if (headingMatch !== null) {
      const hashes = headingMatch[1]?.length ?? 3;
      const level = Math.min(4, Math.max(1, hashes)) as 1 | 2 | 3 | 4;
      blocks.push({
        type: "heading",
        level,
        text: headingMatch[2] ?? "",
      });
      i += 1;
      continue;
    }

    // GFM Table
    if (
      trimmed.includes("|") &&
      i + 1 < lines.length &&
      isTableSeparator(lines[i + 1] ?? "")
    ) {
      const headers = splitTableRow(rawLine);
      i += 2; // skip header + separator
      const rows: string[][] = [];
      while (i < lines.length) {
        const rowLine = (lines[i] ?? "").trim();
        if (rowLine === "" || !rowLine.includes("|")) {
          break;
        }
        rows.push(splitTableRow(rowLine));
        i += 1;
      }
      blocks.push({ type: "table", headers, rows });
      continue;
    }

    // Blockquote
    if (trimmed.startsWith(">")) {
      const quoteLines: string[] = [];
      while (i < lines.length && (lines[i] ?? "").trim().startsWith(">")) {
        quoteLines.push((lines[i] ?? "").trim().replace(/^>\s?/, ""));
        i += 1;
      }
      blocks.push({ type: "blockquote", lines: quoteLines });
      continue;
    }

    // Ordered list (1. item) with optional indented sub-bullets
    if (/^\d+\.\s+/.test(trimmed)) {
      const items: ListItemNode[] = [];
      while (i < lines.length) {
        const line = lines[i] ?? "";
        const lineTrimmed = line.trim();
        const orderedMatch = /^\d+\.\s+(.+)$/.exec(lineTrimmed);
        if (orderedMatch !== null && !/^\s{2,}/.test(line)) {
          items.push({ text: orderedMatch[1] ?? "", children: [] });
          i += 1;
          continue;
        }
        const subBulletMatch = /^\s{2,}(?:[-*+]|\d+\.)\s+(.+)$/.exec(line);
        if (subBulletMatch !== null && items.length > 0) {
          items[items.length - 1]?.children.push(subBulletMatch[1] ?? "");
          i += 1;
          continue;
        }
        break;
      }
      blocks.push({ type: "ol", items });
      continue;
    }

    // Unordered list (- item / * item) with optional indented sub-bullets
    if (/^[-*+]\s+/.test(trimmed)) {
      const items: ListItemNode[] = [];
      while (i < lines.length) {
        const line = lines[i] ?? "";
        const lineTrimmed = line.trim();
        const bulletMatch = /^[-*+]\s+(.+)$/.exec(lineTrimmed);
        if (bulletMatch !== null && !/^\s{2,}/.test(line)) {
          items.push({ text: bulletMatch[1] ?? "", children: [] });
          i += 1;
          continue;
        }
        const subBulletMatch = /^\s{2,}(?:[-*+]|\d+\.)\s+(.+)$/.exec(line);
        if (subBulletMatch !== null && items.length > 0) {
          items[items.length - 1]?.children.push(subBulletMatch[1] ?? "");
          i += 1;
          continue;
        }
        break;
      }
      blocks.push({ type: "ul", items });
      continue;
    }

    // Paragraph (collect consecutive plain text lines)
    const paraLines: string[] = [];
    while (i < lines.length) {
      const nextRaw = lines[i] ?? "";
      const nextTrimmed = nextRaw.trim();
      if (
        nextTrimmed === "" ||
        nextTrimmed.startsWith("```") ||
        /^(#{1,4})\s+/.test(nextTrimmed) ||
        /^(-{3,}|\*{3,}|_{3,})$/.test(nextTrimmed) ||
        nextTrimmed.startsWith(">") ||
        /^\d+\.\s+/.test(nextTrimmed) ||
        /^[-*+]\s+/.test(nextTrimmed) ||
        (nextTrimmed.includes("|") &&
          i + 1 < lines.length &&
          isTableSeparator(lines[i + 1] ?? ""))
      ) {
        break;
      }
      paraLines.push(nextTrimmed);
      i += 1;
    }
    if (paraLines.length > 0) {
      blocks.push({ type: "paragraph", text: paraLines.join("\n") });
    } else {
      i += 1;
    }
  }

  return blocks;
}

const INLINE_TOKEN_REGEX =
  /(`[^`]+`|\*\*[^*]+\*\*|\[[^\]]+\]\(\/[a-zA-Z0-9/_-]*\)|\*[^*\n]+\*)/g;

function renderInline(text: string, keyPrefix: string): ReactNode[] {
  const parts = text.split(INLINE_TOKEN_REGEX);
  return parts
    .filter((part) => part !== "")
    .map((part, idx) => {
      const key = `${keyPrefix}-${String(idx)}`;
      if (part.startsWith("`") && part.endsWith("`") && part.length >= 2) {
        const inner = part.slice(1, -1);
        return (
          <code
            key={key}
            className="bg-muted text-foreground rounded px-1 py-0.5 font-mono text-[11px]"
          >
            {inner}
          </code>
        );
      }
      if (part.startsWith("**") && part.endsWith("**") && part.length >= 4) {
        return (
          <strong key={key} className="text-foreground font-semibold">
            {part.slice(2, -2)}
          </strong>
        );
      }
      if (part.startsWith("[") && part.endsWith(")")) {
        const linkMatch = /^\[([^\]]+)\]\((\/[a-zA-Z0-9/_-]*)\)$/.exec(part);
        if (linkMatch !== null) {
          return (
            <Link
              key={key}
              to={resolveAssistantRoutePath(linkMatch[2] ?? "/")}
              className="text-primary font-medium underline underline-offset-4 hover:opacity-80"
            >
              {linkMatch[1]}
            </Link>
          );
        }
      }
      if (part.startsWith("*") && part.endsWith("*") && part.length >= 2) {
        return (
          <em key={key} className="italic">
            {part.slice(1, -1)}
          </em>
        );
      }
      return <span key={key}>{part}</span>;
    });
}

function renderListItems(items: ListItemNode[], keyPrefix: string) {
  return items.map((item, idx) => {
    const liKey = `${keyPrefix}-li-${String(idx)}`;
    return (
      <li key={liKey} className="leading-relaxed">
        <span>{renderInline(item.text, liKey)}</span>
        {item.children.length > 0 && (
          <ul className="mt-1 list-disc space-y-0.5 pl-4">
            {item.children.map((sub, subIdx) => {
              const subKey = `${keyPrefix}-sub-${String(idx)}-${String(subIdx)}`;
              return (
                <li key={subKey} className="leading-relaxed">
                  {renderInline(sub, subKey)}
                </li>
              );
            })}
          </ul>
        )}
      </li>
    );
  });
}

export function AssistantMarkdown({
  content,
  className,
  isStreaming = false,
}: AssistantMarkdownProps) {
  const blocks = parseBlocks(content);

  return (
    <div
      data-testid="assistant-markdown"
      className={`flex flex-col gap-2 text-xs leading-relaxed ${className ?? ""}`}
    >
      {blocks.map((block, idx) => {
        const blockKey = `md-block-${String(idx)}`;
        const isLastBlock = idx === blocks.length - 1;
        switch (block.type) {
          case "heading": {
            const headingClass =
              block.level <= 2
                ? "text-foreground text-sm font-semibold tracking-tight"
                : "text-foreground text-xs font-semibold tracking-tight";
            return block.level <= 2 ? (
              <h3 key={blockKey} className={headingClass}>
                {renderInline(block.text, blockKey)}
              </h3>
            ) : (
              <h4 key={blockKey} className={headingClass}>
                {renderInline(block.text, blockKey)}
              </h4>
            );
          }
          case "code":
            return (
              <CodeBlock key={blockKey} size="sm">
                <CodeBlockHeader>
                  <CodeBlockTitle>
                    {block.language !== "" ? block.language : "code"}
                  </CodeBlockTitle>
                  <CodeBlockActions>
                    <CodeBlockCopyButton value={block.code} />
                  </CodeBlockActions>
                </CodeBlockHeader>
                <CodeBlockContent wrap className="max-h-64">
                  {block.code}
                </CodeBlockContent>
              </CodeBlock>
            );
          case "blockquote":
            return (
              <blockquote
                key={blockKey}
                className="border-primary/40 text-muted-foreground border-l-2 pl-3 italic"
              >
                {block.lines.map((line, lineIdx) => {
                  const qKey = `${blockKey}-q-${String(lineIdx)}`;
                  return <p key={qKey}>{renderInline(line, qKey)}</p>;
                })}
              </blockquote>
            );
          case "ul":
            return (
              <ul key={blockKey} className="list-disc space-y-1 pl-4">
                {renderListItems(block.items, blockKey)}
              </ul>
            );
          case "ol":
            return (
              <ol key={blockKey} className="list-decimal space-y-1 pl-4">
                {renderListItems(block.items, blockKey)}
              </ol>
            );
          case "table":
            return (
              <div
                key={blockKey}
                className="border-border overflow-x-auto rounded-md border"
              >
                <table className="w-full border-collapse text-left text-xs">
                  <thead className="bg-muted/60 text-foreground border-border border-b">
                    <tr>
                      {block.headers.map((header, hIdx) => {
                        const thKey = `${blockKey}-th-${String(hIdx)}`;
                        return (
                          <th key={thKey} className="px-2.5 py-1.5 font-semibold">
                            {renderInline(header, thKey)}
                          </th>
                        );
                      })}
                    </tr>
                  </thead>
                  <tbody className="divide-border divide-y">
                    {block.rows.map((row, rIdx) => {
                      const trKey = `${blockKey}-tr-${String(rIdx)}`;
                      return (
                        <tr key={trKey}>
                          {row.map((cell, cIdx) => {
                            const tdKey = `${blockKey}-td-${String(rIdx)}-${String(cIdx)}`;
                            return (
                              <td key={tdKey} className="px-2.5 py-1.5">
                                {renderInline(cell, tdKey)}
                              </td>
                            );
                          })}
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            );
          case "hr":
            return <hr key={blockKey} className="border-border my-1" />;
          case "paragraph":
            return (
              <p key={blockKey} className="whitespace-pre-wrap leading-relaxed">
                {renderInline(block.text, blockKey)}
                {isStreaming && isLastBlock && (
                  <span
                    aria-hidden="true"
                    className="bg-primary ml-0.5 inline-block h-3.5 w-1.5 align-middle animate-pulse"
                  />
                )}
              </p>
            );
        }
      })}
      {isStreaming && blocks.length === 0 && (
        <span
          aria-hidden="true"
          className="bg-primary inline-block h-3.5 w-1.5 align-middle animate-pulse"
        />
      )}
    </div>
  );
}
