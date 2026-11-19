import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { EditorContent, useEditor, useEditorState, type Editor } from "@tiptap/react";
import StarterKit from "@tiptap/starter-kit";
import { Markdown } from "@tiptap/markdown";
import {
  Bold,
  Code,
  Heading1,
  Heading2,
  Heading3,
  Italic,
  Link2,
  List,
  ListOrdered,
  Redo2,
  Undo2,
} from "lucide-react";
import { Button } from "@/shared/components/ui/button";
import { Input } from "@/shared/components/ui/input";
import { cn } from "@/shared/lib/utils";

/**
 * Rich-text editor (reference: Shadcn Dashboard blog editor — rounded toolbar
 * strip over a bordered "Start typing…" area). The value stays a Markdown
 * string: the shared contract carries plain text (≤ maxLength), every other
 * page renders it as text, and no HTML is ever stored or rendered. Underline
 * and images are left out on purpose — Markdown has no underline, and an
 * inline image would need an upload path the contract does not have.
 */

export interface MarkdownEditorProps {
  value: string;
  onChange: (markdown: string) => void;
  id?: string;
  maxLength?: number;
  placeholder?: string;
  ariaLabel?: string;
  "data-testid"?: string;
}

const CONTENT_CLASS = [
  "min-h-24 px-3 py-2 text-sm outline-none",
  "[&_p]:my-1 [&_h1]:my-2 [&_h1]:text-xl [&_h1]:font-semibold",
  "[&_h2]:my-2 [&_h2]:text-lg [&_h2]:font-semibold [&_h3]:my-2 [&_h3]:text-base [&_h3]:font-semibold",
  "[&_ul]:my-1 [&_ul]:list-disc [&_ul]:pl-5 [&_ol]:my-1 [&_ol]:list-decimal [&_ol]:pl-5",
  "[&_a]:text-primary [&_a]:underline [&_code]:bg-muted [&_code]:rounded [&_code]:px-1 [&_code]:font-mono",
  "[&_pre]:bg-muted [&_pre]:rounded-md [&_pre]:p-2 [&_pre]:font-mono",
].join(" ");

function ToolbarButton({
  label,
  active = false,
  disabled = false,
  onClick,
  children,
}: {
  label: string;
  active?: boolean;
  disabled?: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <Button
      type="button"
      variant={active ? "secondary" : "ghost"}
      size="icon-sm"
      aria-label={label}
      aria-pressed={active}
      title={label}
      disabled={disabled}
      onMouseDown={(event) => { event.preventDefault(); }}
      onClick={onClick}
    >
      {children}
    </Button>
  );
}

function Toolbar({ editor }: { editor: Editor }) {
  const { t } = useTranslation();
  const [linkOpen, setLinkOpen] = useState(false);
  const [href, setHref] = useState("");
  const state = useEditorState({
    editor,
    selector: ({ editor: e }) => ({
      bold: e.isActive("bold"),
      italic: e.isActive("italic"),
      h1: e.isActive("heading", { level: 1 }),
      h2: e.isActive("heading", { level: 2 }),
      h3: e.isActive("heading", { level: 3 }),
      bullet: e.isActive("bulletList"),
      ordered: e.isActive("orderedList"),
      link: e.isActive("link"),
      code: e.isActive("code"),
      canUndo: e.can().undo(),
      canRedo: e.can().redo(),
    }),
  });
  const chain = () => editor.chain().focus();

  const applyLink = (): void => {
    const url = href.trim();
    if (url === "") chain().unsetLink().run();
    else if (/^(https?:\/\/|mailto:)/i.test(url)) chain().extendMarkRange("link").setLink({ href: url }).run();
    setLinkOpen(false);
    setHref("");
  };

  return (
    <div className="flex flex-col gap-2 rounded-lg border bg-background/40 p-1" data-testid="markdown-toolbar">
      <div className="flex flex-wrap items-center gap-0.5">
        <ToolbarButton label={t("editor.bold")} active={state.bold} onClick={() => { chain().toggleBold().run(); }}><Bold /></ToolbarButton>
        <ToolbarButton label={t("editor.italic")} active={state.italic} onClick={() => { chain().toggleItalic().run(); }}><Italic /></ToolbarButton>
        <ToolbarButton label="H1" active={state.h1} onClick={() => { chain().toggleHeading({ level: 1 }).run(); }}><Heading1 /></ToolbarButton>
        <ToolbarButton label="H2" active={state.h2} onClick={() => { chain().toggleHeading({ level: 2 }).run(); }}><Heading2 /></ToolbarButton>
        <ToolbarButton label="H3" active={state.h3} onClick={() => { chain().toggleHeading({ level: 3 }).run(); }}><Heading3 /></ToolbarButton>
        <ToolbarButton label={t("editor.bulletList")} active={state.bullet} onClick={() => { chain().toggleBulletList().run(); }}><List /></ToolbarButton>
        <ToolbarButton label={t("editor.orderedList")} active={state.ordered} onClick={() => { chain().toggleOrderedList().run(); }}><ListOrdered /></ToolbarButton>
        <ToolbarButton
          label={t("editor.link")}
          active={state.link || linkOpen}
          onClick={() => {
            setHref((editor.getAttributes("link").href as string | undefined) ?? "");
            setLinkOpen((open) => !open);
          }}
        >
          <Link2 />
        </ToolbarButton>
        <ToolbarButton label={t("editor.code")} active={state.code} onClick={() => { chain().toggleCode().run(); }}><Code /></ToolbarButton>
        <ToolbarButton label={t("editor.undo")} disabled={!state.canUndo} onClick={() => { chain().undo().run(); }}><Undo2 /></ToolbarButton>
        <ToolbarButton label={t("editor.redo")} disabled={!state.canRedo} onClick={() => { chain().redo().run(); }}><Redo2 /></ToolbarButton>
      </div>
      {linkOpen && (
        <div className="flex items-center gap-2 px-1 pb-1">
          <Input
            value={href}
            onChange={(event) => { setHref(event.target.value); }}
            onKeyDown={(event) => {
              if (event.key === "Enter") { event.preventDefault(); applyLink(); }
            }}
            placeholder="https://"
            aria-label={t("editor.linkUrl")}
            data-testid="markdown-link-input"
          />
          <Button type="button" size="sm" variant="outline" onClick={applyLink} data-testid="markdown-link-apply">
            {t("editor.linkApply")}
          </Button>
        </div>
      )}
    </div>
  );
}

export function MarkdownEditor({
  value,
  onChange,
  id,
  maxLength,
  placeholder,
  ariaLabel,
  "data-testid": testId,
}: MarkdownEditorProps) {
  const editor = useEditor({
    extensions: [
      StarterKit.configure({ underline: false, link: { openOnClick: false } }),
      Markdown,
    ],
    content: value,
    contentType: "markdown",
    editorProps: {
      attributes: {
        ...(id === undefined ? {} : { id }),
        ...(ariaLabel === undefined ? {} : { "aria-label": ariaLabel }),
        role: "textbox",
        "aria-multiline": "true",
        class: CONTENT_CLASS,
      },
    },
    onUpdate: ({ editor: e }) => {
      const markdown = e.getMarkdown().trimEnd();
      if (maxLength !== undefined && markdown.length > maxLength) {
        e.commands.undo();
        return;
      }
      onChange(markdown);
    },
  });

  // Hydration / external resets (draft load, adoption race): push the new
  // value in without echoing it back through onUpdate.
  useEffect(() => {
    if (editor.isDestroyed) return;
    if (editor.getMarkdown().trimEnd() === value) return;
    editor.commands.setContent(value, { contentType: "markdown", emitUpdate: false });
  }, [editor, value]);

  return (
    <div className="flex flex-col gap-2 rounded-lg border p-2" data-testid={testId}>
      <Toolbar editor={editor} />
      <div className="relative rounded-lg border">
        {editor.isEmpty && placeholder !== undefined && (
          <p className="text-muted-foreground pointer-events-none absolute top-2 left-3 text-sm">{placeholder}</p>
        )}
        <EditorContent editor={editor} />
      </div>
      {maxLength !== undefined && (
        <p
          className={cn("text-right text-xs", value.length >= maxLength ? "text-destructive" : "text-muted-foreground")}
          aria-live="polite"
        >
          {value.length} / {maxLength}
        </p>
      )}
    </div>
  );
}
