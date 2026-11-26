import { useId, useMemo, useRef, useState, type FocusEvent } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { Check, Search, X } from "lucide-react";
import { enUS, zhCN, type Locale } from "react-day-picker/locale";
import { Badge } from "@/shared/components/ui/badge";
import { Calendar } from "@/shared/components/ui/calendar";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
} from "@/shared/components/ui/input-group";
import { usePortalAnchor } from "@/shared/lib/use-portal-anchor";
import { cn } from "@/shared/lib/utils";

/**
 * ECS-style global search bar (owner feedback 2026-11-04: global search bar, modeled after
 * Alibaba Cloud ECS console). One control covers every list column: free text matches
 * any field, or the user scopes a term to a field first (order number / title / state
 * / submitter / datasource...) and the term lands as a removable chip. Enum fields can
 * carry value suggestions picked from a list.
 *
 * The bar is a pure terms editor — it never touches the rows itself:
 * - `terms` are the committed chips (controlled by the page).
 * - `onLiveChange` reports the uncommitted input text on every keystroke so
 *   client-filtered pages keep filtering while the user types (Enter turns
 *   the live text into a chip); server-filtered pages debounce it into their
 *   existing query params.
 * `filterBySearchTerms` is the matching half for client-filtered pages.
 */

export interface SearchFieldOption {
  /** User-visible value (matched case-insensitively as a substring). */
  label: string;
  /** Optional locale-independent canonical token preserved across runtime locale switches. */
  value?: string;
}

export interface SearchFieldDef {
  key: string;
  label: string;
  /** Enum suggestions; free-text fields simply omit them. */
  options?: readonly SearchFieldOption[];
  /** Calendar-backed field (owner feedback 2026-11-05): the value comes from
   * the inline date panel — typed text is never committed for these. */
  input?: "date";
}

/** One committed condition: a field key (null = any field) plus display text and optional canonical value. */
export interface SearchTerm {
  field: string | null;
  text: string;
  value?: string;
}

export interface SearchAccessor<T> extends SearchFieldDef {
  /** Substring a row exposes for this field — place canonical enum `value`
   * token first before localized labels so canonical chip filters match exactly. */
  value: (item: T) => string;
}

const normalize = (text: string): string => text.trim().toLowerCase();

/** Local-day helpers — chip values are plain YYYY-MM-DD strings (same
 * convention as the DatePicker filters this bar replaces). */
function parseLocalDay(value: string): Date {
  const [year, month, day] = value.split("-").map(Number);
  return new Date(year ?? 1970, (month ?? 1) - 1, day ?? 1);
}

function toLocalDay(date: Date): string {
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${String(date.getFullYear())}-${month}-${day}`;
}

function matchesTerm(raw: string, term: SearchTerm): boolean {
  if (term.value !== undefined) {
    const firstToken = raw.trim().toLowerCase().split(/\s+/)[0] ?? "";
    return firstToken === normalize(term.value);
  }
  return raw.toLowerCase().includes(normalize(term.text));
}

export function filterBySearchTerms<T>(
  items: readonly T[],
  terms: readonly SearchTerm[],
  accessors: readonly SearchAccessor<T>[],
): T[] {
  const active = terms.filter((term) => normalize(term.value ?? term.text) !== "");
  if (active.length === 0) return [...items];
  return items.filter((item) =>
    active.every((term) => {
      if (term.field === null) {
        return accessors.some((accessor) => matchesTerm(accessor.value(item), term));
      }
      const accessor = accessors.find((candidate) => candidate.key === term.field);
      return accessor !== undefined && matchesTerm(accessor.value(item), term);
    }),
  );
}

type SuggestionRow =
  | { kind: "all"; text: string }
  | { kind: "field"; field: SearchFieldDef; text: string }
  | { kind: "value"; field: SearchFieldDef; option: SearchFieldOption };

export function GlobalSearchBar({
  fields,
  terms,
  onTermsChange,
  onLiveChange,
  placeholder,
  testId,
  className,
  resetKey,
}: {
  fields: readonly SearchFieldDef[];
  terms: readonly SearchTerm[];
  onTermsChange: (terms: SearchTerm[]) => void;
  onLiveChange?: (live: SearchTerm | null) => void;
  placeholder?: string;
  testId?: string;
  className?: string;
  /** Bump to clear the input (toolbar reset clears chips server-side but
   * the draft text lives in this component's local state). */
  resetKey?: number;
}) {
  const { t, i18n } = useTranslation();
  const locale: Locale = i18n.language.startsWith("zh") ? zhCN : enUS;
  const listId = useId();
  const datePanelId = `${listId}-date`;
  const inputRef = useRef<HTMLInputElement>(null);
  const [draft, setDraft] = useState("");
  const [pendingField, setPendingField] = useState<SearchFieldDef | null>(null);
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(0);

  // Toolbar reset: chips are the page's state, the draft text is ours —
  // a resetKey bump empties the input so the bar and the params agree.
  // Render-phase state adjust (react.dev pattern for deriving state from
  // props; same shape as result-grid's tab-key reset).
  const [appliedResetKey, setAppliedResetKey] = useState(resetKey);
  if (appliedResetKey !== resetKey) {
    setAppliedResetKey(resetKey);
    setDraft("");
    setPendingField(null);
  }

  const rows = useMemo<SuggestionRow[]>(() => {
    if (pendingField !== null) {
      // Date fields have no suggestion list — the panel switches to the
      // inline calendar instead.
      if (pendingField.input === "date") return [];
      const needle = normalize(draft);
      return (pendingField.options ?? [])
        .filter((option) => normalize(option.label).includes(needle))
        .map((option): SuggestionRow => ({ kind: "value", field: pendingField, option }));
    }
    const needle = normalize(draft);
    const matchingFields = fields.filter((field) => normalize(field.label).includes(needle));
    if (needle === "") {
      return matchingFields.map((field): SuggestionRow => ({ kind: "field", field, text: "" }));
    }
    return [
      { kind: "all", text: draft },
      ...matchingFields.map((field): SuggestionRow => ({ kind: "field", field, text: draft })),
    ];
  }, [draft, fields, pendingField]);

  const reportLive = (text: string, field: SearchFieldDef | null): void => {
    onLiveChange?.(normalize(text) === "" ? null : { field: field?.key ?? null, text });
  };

  const commit = (term: SearchTerm): void => {
    // One any-field term at a time (review M4): a new free-text chip
    // REPLACES the previous one — pages map it to a single server keyword
    // (the q param), so a second appended chip would display without ever
    // reaching the request. A scoped chip is likewise single-value-per-field
    // (state/datasource/date params each accept exactly one value), so a new
    // value for the same field replaces the old chip; identical canonical
    // identity is a no-op. The index-suffixed chip keys stay unique either way.
    const identity = (candidate: SearchTerm): string => candidate.value ?? candidate.text;
    const next =
      term.field === null
        ? [...terms.filter((existing) => existing.field !== null), term]
        : terms.some(
              (existing) => existing.field === term.field && identity(existing) === identity(term),
            )
          ? [...terms]
          : [...terms.filter((existing) => existing.field !== term.field), term];
    onTermsChange(next);
    setDraft("");
    setPendingField(null);
    setActiveIndex(0);
    setOpen(false);
    reportLive("", null);
    inputRef.current?.focus();
  };

  const activateRow = (row: SuggestionRow): void => {
    if (row.kind === "all") {
      commit({ field: null, text: row.text });
      return;
    }
    if (row.kind === "field") {
      if (row.field.options !== undefined) {
        // Enum field (review L5): the value must come from the option list
        // so the label→enum mapping stays exact. Scoping keeps the draft as
        // an option filter instead of committing it as a free-text term.
        setPendingField(row.field);
        setActiveIndex(0);
        reportLive(draft, row.field);
        inputRef.current?.focus();
        return;
      }
      if (row.field.input === "date") {
        // Date field: scope to the calendar panel; typed text is never the
        // value source, so any draft is dropped on scoping.
        setPendingField(row.field);
        setDraft("");
        setActiveIndex(0);
        reportLive("", row.field);
        inputRef.current?.focus();
        return;
      }
      if (normalize(row.text) !== "") {
        commit({ field: row.field.key, text: row.text });
      } else {
        // Free-text field (owner feedback 2026-11-05): once the field is
        // chosen, close the dropdown so the user can type directly into the
        // scoped input without a lingering popup below.
        setPendingField(row.field);
        setDraft("");
        setActiveIndex(0);
        setOpen(false);
        reportLive("", row.field);
        inputRef.current?.focus();
      }
      return;
    }
    commit({
      field: row.field.key,
      text: row.option.label,
      ...(row.option.value !== undefined ? { value: row.option.value } : {}),
    });
  };

  const removeTerm = (index: number): void => {
    onTermsChange(terms.filter((_, current) => current !== index));
    // Re-report the draft (review M2): pages derive their live keyword from
    // the last onLiveChange, so pruning a chip without re-reporting would
    // reset their filter while the input still shows text.
    reportLive(draft, pendingField);
    inputRef.current?.focus();
  };

  const clearAll = (): void => {
    onTermsChange([]);
    setDraft("");
    setPendingField(null);
    reportLive("", null);
    inputRef.current?.focus();
  };

  const hasContent = terms.length > 0 || draft !== "";
  const isListboxOpen = open && rows.length > 0;
  const isEmptyMenuOpen = open && rows.length === 0 && pendingField?.options !== undefined;
  const isDatePanelOpen = open && pendingField !== null && pendingField.input === "date";
  const activePopupId = isListboxOpen ? listId : isDatePanelOpen ? datePanelId : undefined;
  const containerRef = useRef<HTMLDivElement>(null);
  const popupRef = useRef<HTMLElement | null>(null);
  const portalKey: string | boolean = isListboxOpen
    ? `list:${pendingField?.key ?? "__fields__"}`
    : isEmptyMenuOpen
      ? `empty:${pendingField.key}`
      : isDatePanelOpen
        ? `date:${pendingField.key}`
        : false;
  usePortalAnchor(portalKey, containerRef, popupRef, {
    matchWidth: !isDatePanelOpen,
    fallbackWidth: isDatePanelOpen ? 280 : 240,
    preferredHeight: isDatePanelOpen ? 360 : 256,
    gap: isDatePanelOpen ? 6 : 4,
  });
  const handleContainerBlur = (event: FocusEvent<HTMLElement>): void => {
    const related = event.relatedTarget as Node | null;
    if (
      !containerRef.current?.contains(related) &&
      !popupRef.current?.contains(related)
    ) {
      setOpen(false);
    }
  };
  const fieldLabel = (key: string | null): string =>
    key === null ? "" : fields.find((field) => field.key === key)?.label ?? key;
  const termDisplayText = (term: SearchTerm): string => {
    if (term.field === null || term.value === undefined) return term.text;
    return fields
      .find((field) => field.key === term.field)
      ?.options?.find((option) => option.value === term.value)?.label ?? term.text;
  };

  return (
    <div
      ref={containerRef}
      className={cn("relative min-w-0", className)}
      onBlur={handleContainerBlur}
    >
      <InputGroup className="h-auto min-h-8 flex-wrap py-1">
        <InputGroupAddon>
          <Search aria-hidden />
        </InputGroupAddon>
        {pendingField !== null && (
          <Badge
            variant="secondary"
            className="gap-0.5 pr-0.5 animate-in fade-in-0 zoom-in-95 duration-150 ease-out"
          >
            {pendingField.label}
            <InputGroupButton
              size="icon-xs"
              className="size-4 p-0"
              aria-label={t("search.unscopeField", { field: pendingField.label })}
              onClick={() => {
                setPendingField(null);
                reportLive(draft, null);
                inputRef.current?.focus();
              }}
              data-testid={`${testId ?? "global-search"}-unscope`}
            >
              <X aria-hidden />
            </InputGroupButton>
          </Badge>
        )}
        {terms.map((term, index) => {
          const display = termDisplayText(term);
          return (
            <Badge
              key={`${term.field ?? "*"}:${term.value ?? term.text}:${String(index)}`}
              variant="secondary"
              className="gap-0.5 pr-0.5 animate-in fade-in-0 zoom-in-95 duration-150 ease-out"
            >
              <span className="max-w-40 truncate">
                {term.field === null ? display : `${fieldLabel(term.field)}: ${display}`}
              </span>
              <InputGroupButton
                size="icon-xs"
                className="size-4 p-0"
                aria-label={t("search.removeTerm", {
                  term: term.field === null ? display : `${fieldLabel(term.field)}: ${display}`,
                })}
                onClick={() => { removeTerm(index); }}
              >
                <X aria-hidden />
              </InputGroupButton>
            </Badge>
          );
        })}
        <InputGroupInput
          ref={inputRef}
          role="combobox"
          aria-expanded={activePopupId !== undefined}
          aria-controls={activePopupId}
          aria-haspopup={pendingField?.input === "date" ? "dialog" : "listbox"}
          aria-activedescendant={
            isListboxOpen && rows[activeIndex] !== undefined
              ? `${listId}-option-${String(activeIndex)}`
              : undefined
          }
          aria-autocomplete="list"
          aria-label={placeholder ?? t("search.placeholderAll")}
          value={draft}
          placeholder={
            pendingField !== null
              ? pendingField.input === "date"
                ? t("search.pickDate")
                : pendingField.options !== undefined
                  ? t("search.fieldValuePlaceholder", { field: pendingField.label })
                  : t("search.typeValueHint", { field: pendingField.label })
              : (placeholder ?? t("search.placeholderAll"))
          }
          onFocus={() => { setOpen(true); }}
          // Reopening by click: after Escape or a picked date the input still
          // holds focus, so the second click must also open the panel.
          onClick={() => { setOpen(true); }}
          onChange={(event) => {
            setDraft(event.target.value);
            setActiveIndex(0);
            setOpen(true);
            reportLive(event.target.value, pendingField);
          }}
          onKeyDown={(event) => {
            // IME composition (review M3): keydown fires with isComposing
            // while a CJK candidate is being confirmed — that Enter belongs
            // to the IME, not to the search bar.
            if (event.nativeEvent.isComposing) {
              return;
            }
            if (event.key === "ArrowDown") {
              event.preventDefault();
              setOpen(true);
              if (rows.length > 0) {
                setActiveIndex((current) => (current + 1) % rows.length);
              }
            } else if (event.key === "ArrowUp") {
              event.preventDefault();
              if (rows.length > 0) {
                setActiveIndex((current) => (current - 1 + rows.length) % rows.length);
              }
            } else if (event.key === "Enter") {
              event.preventDefault();
              // Date fields only commit through the calendar — Enter must not
              // turn typed text into a date chip.
              if (pendingField?.input === "date") {
                return;
              }
              if (open && rows.length > 0 && rows[activeIndex] !== undefined) {
                activateRow(rows[activeIndex]);
              } else if (normalize(draft) !== "") {
                if (pendingField?.options !== undefined) {
                  // Enum field (review L5): only an exact option label may
                  // become a chip — anything else must be picked from the
                  // list so the page's label→enum mapping never guesses.
                  const exact = pendingField.options.find(
                    (option) => normalize(option.label) === normalize(draft),
                  );
                  if (exact !== undefined) {
                    commit({
                      field: pendingField.key,
                      text: exact.label,
                      ...(exact.value !== undefined ? { value: exact.value } : {}),
                    });
                  }
                } else {
                  commit({ field: pendingField?.key ?? null, text: draft });
                }
              }
            } else if (event.key === "Escape") {
              if (draft === "" && pendingField !== null) {
                setPendingField(null);
                reportLive("", null);
              } else {
                setOpen(false);
              }
            } else if (event.key === "Backspace" && draft === "" && terms.length > 0) {
              event.preventDefault();
              removeTerm(terms.length - 1);
            }
          }}
          data-testid={testId}
        />
        {hasContent && (
          <InputGroupAddon align="inline-end">
            <InputGroupButton
              aria-label={t("search.clearAll")}
              onClick={clearAll}
              size="icon-xs"
              data-testid={testId !== undefined ? `${testId}-clear` : undefined}
            >
              <X aria-hidden />
            </InputGroupButton>
          </InputGroupAddon>
        )}
      </InputGroup>
      {isListboxOpen && typeof document !== "undefined" &&
        createPortal(
          <ul
            ref={(node) => { popupRef.current = node; }}
            key={pendingField?.key ?? "__fields__"}
            id={listId}
            role="listbox"
            aria-label={t("search.filterByField")}
            className="fixed z-[60] max-h-64 origin-top overflow-x-hidden overflow-y-auto overscroll-contain rounded-md border border-border bg-popover p-1 text-popover-foreground shadow-md animate-in fade-in-0 zoom-in-95 slide-in-from-top-2 duration-[220ms] ease-out [scrollbar-width:thin]"
            data-testid={testId !== undefined ? `${testId}-menu` : undefined}
            onBlur={handleContainerBlur}
          >
            {pendingField === null && (
              <li role="presentation" className="px-2 py-1 text-xs font-medium text-muted-foreground">
                {t("search.filterByField")}
              </li>
            )}
            {rows.map((row, index) => {
              const active = index === activeIndex;
              const key =
                row.kind === "all"
                  ? "all"
                  : row.kind === "field"
                    ? `field:${row.field.key}`
                    : `value:${row.field.key}:${row.option.value ?? row.option.label}`;
              return (
                <li
                  key={key}
                  id={`${listId}-option-${String(index)}`}
                  role="option"
                  aria-selected={active}
                  style={{ animationDelay: `${String(Math.min(index * 25, 150))}ms` }}
                  className="flex cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-sm transition-[background-color,color,translate] duration-150 ease-out animate-in fade-in-0 slide-in-from-top-1 fill-mode-both data-[active=true]:translate-x-0.5 data-[active=true]:bg-accent data-[active=true]:text-accent-foreground"
                  data-active={active}
                  onMouseEnter={() => { setActiveIndex(index); }}
                  onMouseDown={(event) => { event.preventDefault(); }}
                  onClick={() => { activateRow(row); }}
                >
                  {row.kind === "all" && (
                    <Search className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                  )}
                  {row.kind === "field" && normalize(row.text) !== "" && (
                    <span className="max-w-24 shrink-0 truncate text-xs text-muted-foreground">
                      {row.field.label}
                    </span>
                  )}
                  {row.kind === "value" && (
                    <Check className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                  )}
                  <span className="min-w-0 flex-1 truncate">
                    {row.kind === "all"
                      ? t("search.searchInAll", { query: row.text })
                      : row.kind === "field"
                        ? (normalize(row.text) !== "" ? row.text : row.field.label)
                        : row.option.label}
                  </span>
                </li>
              );
            })}
          </ul>,
          document.body,
        )}
      {isEmptyMenuOpen && typeof document !== "undefined" &&
        createPortal(
          <div
            ref={(node) => { popupRef.current = node; }}
            key={`empty:${pendingField.key}`}
            className="fixed z-[60] origin-top rounded-md border border-border bg-popover px-2 py-1.5 text-xs text-muted-foreground shadow-md animate-in fade-in-0 zoom-in-95 slide-in-from-top-2 duration-[220ms] ease-out"
            data-testid={testId !== undefined ? `${testId}-menu` : undefined}
            onBlur={handleContainerBlur}
          >
            {t("search.noMatches")}
          </div>,
          document.body,
        )}
      {isDatePanelOpen && typeof document !== "undefined" &&
        createPortal(
          <div
            ref={(node) => { popupRef.current = node; }}
            key={`date:${pendingField.key}`}
            id={datePanelId}
            role="dialog"
            aria-label={`${pendingField.label} — ${t("search.pickDate")}`}
            data-slot="popover-content"
            className="fixed z-[60] w-auto origin-top-left rounded-lg border border-border bg-popover p-0 text-popover-foreground shadow-md animate-in fade-in-0 zoom-in-95 slide-in-from-top-2 duration-[220ms] ease-out"
            data-testid={testId !== undefined ? `${testId}-menu` : undefined}
            onMouseDown={(event) => { event.preventDefault(); }}
            onBlur={handleContainerBlur}
            onKeyDown={(event) => {
              if (event.key === "Escape") {
                setOpen(false);
                inputRef.current?.focus();
              }
            }}
          >
            <Calendar
              mode="single"
              defaultMonth={(() => {
                const text = [...terms]
                  .reverse()
                  .find((term) => term.field === pendingField.key)?.text;
                return text === undefined ? undefined : parseLocalDay(text);
              })()}
              selected={(() => {
                const text = [...terms]
                  .reverse()
                  .find((term) => term.field === pendingField.key)?.text;
                return text === undefined ? undefined : parseLocalDay(text);
              })()}
              onSelect={(day) => {
                if (day === undefined) return;
                commit({ field: pendingField.key, text: toLocalDay(day) });
                setOpen(false);
              }}
              locale={locale}
            />
          </div>,
          document.body,
        )}
    </div>
  );
}
