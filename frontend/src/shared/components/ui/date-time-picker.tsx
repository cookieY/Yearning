import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  CalendarIcon,
  ChevronLeft,
  ChevronRight,
  ChevronsLeft,
  ChevronsRight,
  X,
} from "lucide-react";

import { Button } from "@/shared/components/ui/button";
import { Input } from "@/shared/components/ui/input";
import { cn } from "@/shared/lib/utils";

/**
 * Desktop-suite-style date-time picker (§18.56 addendum 2, owner ruling 2026-10-15):
 * an input with calendar/clear suffixes anchors a dropdown panel — day grid
 * with super-prev/prev/next/super-next navigation and a zoomable month view,
 * a desktop-picker-style footer ("Time" toggles the hour/minute columns, Confirm commits).
 * Everything renders from the local token system (primary fills, muted
 * disabled cells); the panel is a plain conditional layer (no Base UI popup
 * primitive) so jsdom tests can drive it.
 *
 * Value model: "YYYY-MM-DDTHH:mm" ("" = empty). enforceFuture disables past
 * days and past hours/minutes on today — the wall-clock floor is captured
 * once per open (impure work stays in the event handler), render stays pure.
 * Day cells carry ISO aria-labels so assertions are locale-independent.
 */
export function DateTimePicker({
  value,
  onChange,
  enforceFuture = false,
  placeholder,
  ariaLabel,
  testId,
  id,
  className,
  inputClassName,
}: {
  value: string;
  onChange: (next: string) => void;
  /** True when the picked instant must not be before "now" (§18.56). */
  enforceFuture?: boolean;
  placeholder?: string;
  ariaLabel?: string;
  testId?: string;
  id?: string;
  className?: string;
  /** Size override for the trigger input (default h-8 stays for compact hosts). */
  inputClassName?: string;
}) {
  const { t, i18n } = useTranslation();
  const zh = i18n.language?.startsWith("zh") ?? false;
  const rootRef = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  // Floor captured at panel open — pure render derives everything from it.
  const [floor, setFloor] = useState<Date | null>(null);
  const [view, setView] = useState<"date" | "months" | "time">("date");
  // Cursor month shown in the grid.
  const [cursor, setCursor] = useState<{ year: number; month: number }>({ year: 1970, month: 0 });
  // Interim selection: date "YYYY-MM-DD" + time "HH:mm".
  const [pickedDate, setPickedDate] = useState("");
  const [pickedTime, setPickedTime] = useState("");

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent): void => {
      if (rootRef.current !== null && !rootRef.current.contains(event.target as Node)) {
        setOpen(false);
      }
    };
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === "Escape") setOpen(false);
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  const openPanel = (): void => {
    const now = new Date();
    setFloor(now);
    const base = value === ""
      ? new Date(now.getTime() + 60_000)
      : parseValue(value) ?? new Date(now.getTime() + 60_000);
    setPickedDate(toDayKey(base));
    setPickedTime(toTimeKey(base));
    setCursor({ year: base.getFullYear(), month: base.getMonth() });
    setView("date");
    setOpen(true);
  };

  const confirm = (): void => {
    if (pickedDate === "" || pickedTime === "") return;
    onChange(`${pickedDate}T${pickedTime}`);
    setOpen(false);
  };

  const todayKey = floor === null ? "" : toDayKey(floor);
  const dayDisabled = (dayKey: string): boolean =>
    enforceFuture && floor !== null && dayKey < todayKey;
  const hourDisabled = (hour: number): boolean => {
    if (!enforceFuture || floor === null || pickedDate !== todayKey) return false;
    return hour < floor.getHours();
  };
  const minuteDisabled = (hour: number, minute: number): boolean => {
    if (!enforceFuture || floor === null || pickedDate !== todayKey) return false;
    if (hour < floor.getHours()) return true;
    if (hour > floor.getHours()) return false;
    return minute <= floor.getMinutes();
  };
  const confirmable =
    pickedDate !== "" &&
    pickedTime !== "" &&
    !(enforceFuture && floor !== null && new Date(`${pickedDate}T${pickedTime}`).getTime() <= floor.getTime());

  const display = value === "" ? "" : `${value.slice(0, 10)} ${value.slice(11, 16)}`;

  return (
    <div ref={rootRef} className={cn("relative", className)}>
      <div className="relative">
        <Input
          id={id}
          readOnly
          value={display}
          placeholder={placeholder ?? t("dateTimePicker.placeholder")}
          aria-label={ariaLabel}
          onFocus={openPanel}
          onClick={openPanel}
          data-testid={testId}
          className={cn(
            "h-8 w-56 cursor-pointer pr-14 text-xs tabular-nums",
            inputClassName,
          )}
        />
        <span className="pointer-events-none absolute inset-y-0 right-0 flex items-center gap-1 pr-2.5">
          {value !== "" && (
            <button
              type="button"
              aria-label={t("dateTimePicker.clear")}
              className="hover:bg-accent pointer-events-auto flex size-4 items-center justify-center rounded-full"
              onClick={(event) => {
                event.stopPropagation();
                onChange("");
                setOpen(false);
              }}
              data-testid={testId === undefined ? undefined : `${testId}-clear`}
            >
              <X className="text-muted-foreground size-3" aria-hidden />
            </button>
          )}
          <CalendarIcon className="text-muted-foreground size-3.5" aria-hidden />
        </span>
      </div>

      {open && (
        <div
          role="dialog"
          aria-label={t("dateTimePicker.title")}
          className="bg-popover text-popover-foreground absolute top-[calc(100%+4px)] left-0 z-50 w-64 rounded-lg border p-2"
          data-testid={testId === undefined ? undefined : `${testId}-panel`}
        >
          {view === "date" && (
            <DateGrid
              cursor={cursor}
              onCursor={setCursor}
              pickedDate={pickedDate}
              onPick={(dayKey) => { setPickedDate(dayKey); }}
              todayKey={todayKey === "" ? toDayKey(new Date()) : todayKey}
              dayDisabled={dayDisabled}
              zh={zh}
              onTitle={() => { setView("months"); }}
            />
          )}
          {view === "months" && (
            <MonthGrid
              year={cursor.year}
              zh={zh}
              picked={pickedDate.slice(0, 7)}
              onPick={(year, month) => {
                setCursor({ year, month });
                setView("date");
              }}
              onPrevYear={() => { setCursor((c) => ({ ...c, year: c.year - 1 })); }}
              onNextYear={() => { setCursor((c) => ({ ...c, year: c.year + 1 })); }}
            />
          )}
          {view === "time" && (
            <TimeColumns
              pickedTime={pickedTime}
              onPick={(time) => { setPickedTime(time); }}
              hourDisabled={hourDisabled}
              minuteDisabled={minuteDisabled}
            />
          )}

          <div className="mt-1 flex items-center justify-between border-t pt-1.5">
            <button
              type="button"
              className="text-muted-foreground hover:text-foreground flex items-center gap-1 px-1 text-xs"
              onClick={() => { setView(view === "time" ? "date" : "time"); }}
              data-testid={testId === undefined ? undefined : `${testId}-time-toggle`}
            >
              <CalendarIcon className="size-3" aria-hidden />
              <span>{view === "time" ? t("dateTimePicker.backToDate") : t("dateTimePicker.time")}</span>
              {pickedTime !== "" && <span className="tabular-nums">{pickedTime}</span>}
            </button>
            <Button
              type="button"
              size="sm"
              className="h-6 px-2 text-xs"
              disabled={!confirmable}
              onClick={confirm}
              data-testid={testId === undefined ? undefined : `${testId}-confirm`}
            >
              {t("dateTimePicker.confirm")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

/** Day view: << < Oct 2026 > >> + weekday row + 6x7 grid (today in primary text on soft fill, selected day with primary fill). */
function DateGrid({
  cursor,
  onCursor,
  pickedDate,
  onPick,
  todayKey,
  dayDisabled,
  zh,
  onTitle,
}: {
  cursor: { year: number; month: number };
  onCursor: (next: { year: number; month: number }) => void;
  pickedDate: string;
  onPick: (dayKey: string) => void;
  todayKey: string;
  dayDisabled: (dayKey: string) => boolean;
  zh: boolean;
  onTitle: () => void;
}) {
  const { t } = useTranslation();
  const prevMonth = (): void => {
    onCursor(cursor.month === 0 ? { year: cursor.year - 1, month: 11 } : { year: cursor.year, month: cursor.month - 1 });
  };
  const nextMonth = (): void => {
    onCursor(cursor.month === 11 ? { year: cursor.year + 1, month: 0 } : { year: cursor.year, month: cursor.month + 1 });
  };

  // zh_CN starts on Monday; en_US starts on Sunday.
  const weekLabels = useMemo(() => buildWeekLabels(t, zh), [t, zh]);
  const cells = useMemo(
    () => buildMonthCells(cursor.year, cursor.month, zh),
    [cursor.year, cursor.month, zh],
  );

  return (
    <div className="flex flex-col gap-1">
      <div className="grid grid-cols-7 items-center">
        <NavButton label={t("dateTimePicker.prevYear")} onClick={() => { onCursor({ ...cursor, year: cursor.year - 1 }); }}>
          <ChevronsLeft className="size-3.5" aria-hidden />
        </NavButton>
        <NavButton label={t("dateTimePicker.prevMonth")} onClick={prevMonth}>
          <ChevronLeft className="size-3.5" aria-hidden />
        </NavButton>
        <button
          type="button"
          onClick={onTitle}
          className="hover:text-primary col-span-3 text-center text-xs font-medium"
        >
          {zh
            ? t("dateTimePicker.yearMonth", { year: cursor.year, month: cursor.month + 1 })
            : t("dateTimePicker.yearMonth", {
                year: cursor.year,
                month: new Date(cursor.year, cursor.month).toLocaleDateString("en-US", { month: "long" }),
              })}
        </button>
        <NavButton label={t("dateTimePicker.nextMonth")} onClick={nextMonth}>
          <ChevronRight className="size-3.5" aria-hidden />
        </NavButton>
        <NavButton label={t("dateTimePicker.nextYear")} onClick={() => { onCursor({ ...cursor, year: cursor.year + 1 }); }}>
          <ChevronsRight className="size-3.5" aria-hidden />
        </NavButton>
      </div>
      <div className="grid grid-cols-7">
        {weekLabels.map((label) => (
          <div key={label} className="text-muted-foreground flex h-7 items-center justify-center text-xs">
            {label}
          </div>
        ))}
        {cells.map((cell, index) => {
          if (cell === null) return <div key={`empty-${String(index)}`} className="h-8" />;
          const dayKey = `${cell.year}-${pad2(cell.month + 1)}-${pad2(cell.day)}`;
          const selected = dayKey === pickedDate;
          const isToday = dayKey === todayKey;
          const disabled = dayDisabled(dayKey);
          return (
            <button
              key={dayKey}
              type="button"
              aria-label={dayKey}
              disabled={disabled}
              onClick={() => { onPick(dayKey); }}
              className={cn(
                "flex h-8 items-center justify-center rounded text-xs tabular-nums transition-colors",
                disabled && "text-muted-foreground/40 cursor-not-allowed",
                !disabled && !selected && !isToday && "hover:bg-primary/10",
                !disabled && isToday && !selected && "text-primary bg-primary/10 hover:bg-primary/15 font-medium",
                selected && "bg-primary text-primary-foreground hover:bg-primary/90",
              )}
            >
              {cell.day}
            </button>
          );
        })}
      </div>
    </div>
  );
}

/** Month view: << 2026 > >> + 3x4 month grid (selected month with primary fill). */
function MonthGrid({
  year,
  zh,
  picked,
  onPick,
  onPrevYear,
  onNextYear,
}: {
  year: number;
  zh: boolean;
  picked: string;
  onPick: (year: number, month: number) => void;
  onPrevYear: () => void;
  onNextYear: () => void;
}) {
  const { t } = useTranslation();
  const monthLabel = (month: number): string =>
    zh
      ? t("dateTimePicker.monthShort", { month: month + 1 })
      : t("dateTimePicker.monthShort", {
          month: new Date(2026, month).toLocaleDateString("en-US", { month: "short" }),
        });
  return (
    <div className="flex flex-col gap-1">
      <div className="grid grid-cols-7 items-center">
        <NavButton label={t("dateTimePicker.prevYear")} onClick={onPrevYear}>
          <ChevronsLeft className="size-3.5" aria-hidden />
        </NavButton>
        <span className="col-span-5 text-center text-xs font-medium tabular-nums">
          {t("dateTimePicker.yearLabel", { year })}
        </span>
        <NavButton label={t("dateTimePicker.nextYear")} onClick={onNextYear}>
          <ChevronsRight className="size-3.5" aria-hidden />
        </NavButton>
      </div>
      <div className="grid grid-cols-3">
        {Array.from({ length: 12 }, (_, month) => {
          const key = `${String(year)}-${pad2(month + 1)}`;
          const selected = key === picked;
          return (
            <button
              key={key}
              type="button"
              aria-label={key}
              onClick={() => { onPick(year, month); }}
              className={cn(
                "flex h-8 items-center justify-center rounded text-xs transition-colors",
                !selected && "hover:bg-primary/10",
                selected && "bg-primary text-primary-foreground hover:bg-primary/90",
              )}
            >
              {monthLabel(month)}
            </button>
          );
        })}
      </div>
    </div>
  );
}

/** Time columns: hour and minute columns **scroll independently** (selected item with primary fill), scrolling the selected item into view on open. */
function TimeColumns({
  pickedTime,
  onPick,
  hourDisabled,
  minuteDisabled,
}: {
  pickedTime: string;
  onPick: (time: string) => void;
  hourDisabled: (hour: number) => boolean;
  minuteDisabled: (hour: number, minute: number) => boolean;
}) {
  const { t } = useTranslation();
  const hour = pickedTime.slice(0, 2);
  const minute = pickedTime.slice(3, 5);
  return (
    <div className="flex flex-col gap-1">
      <div className="grid grid-cols-2 gap-1">
        <div className="text-muted-foreground text-center text-xs">{t("dateTimePicker.hour")}</div>
        <div className="text-muted-foreground text-center text-xs">{t("dateTimePicker.minute")}</div>
      </div>
      <div className="flex gap-1">
        <ScrollColumn testId="dtp-hours" selected={hour}>
          {Array.from({ length: 24 }, (_, h) => {
            const label = pad2(h);
            const selected = hour === label;
            const disabled = hourDisabled(h);
            return (
              <button
                key={label}
                type="button"
                disabled={disabled}
                onClick={() => { onPick(`${label}:${minute}`); }}
                className={cn(
                  "flex h-8 shrink-0 items-center justify-center rounded text-xs tabular-nums transition-colors",
                  disabled && "text-muted-foreground/40 cursor-not-allowed",
                  !disabled && !selected && "hover:bg-primary/10",
                  selected && "bg-primary text-primary-foreground hover:bg-primary/90",
                )}
              >
                {label}
              </button>
            );
          })}
        </ScrollColumn>
        <ScrollColumn testId="dtp-minutes" selected={minute}>
          {Array.from({ length: 60 }, (_, m) => {
            const label = pad2(m);
            const selected = minute === label;
            const disabled = hour === "" || minuteDisabled(Number(hour), m);
            return (
              <button
                key={label}
                type="button"
                disabled={disabled}
                onClick={() => { onPick(`${hour}:${label}`); }}
                className={cn(
                  "flex h-8 shrink-0 items-center justify-center rounded text-xs tabular-nums transition-colors",
                  disabled && "text-muted-foreground/40 cursor-not-allowed",
                  !disabled && !selected && "hover:bg-primary/10",
                  selected && "bg-primary text-primary-foreground hover:bg-primary/90",
                )}
              >
                {label}
              </button>
            );
          })}
        </ScrollColumn>
      </div>
    </div>
  );
}

/** Independently scrollable column: fixed height with own overflow-y-auto; scrolls the selected item to the middle of the viewport on mount (desktop widget convention). */
function ScrollColumn({ testId, selected, children }: {
  testId: string;
  selected: string;
  children: React.ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const container = ref.current;
    if (container === null) return;
    const item = container.querySelector<HTMLButtonElement>(`button:nth-child(${String(Number(selected) + 1)})`);
    if (item !== null) {
      container.scrollTop = item.offsetTop - container.clientHeight / 2 + item.clientHeight / 2;
    }
  }, [selected]);
  return (
    <div ref={ref} className="flex h-44 flex-1 flex-col overflow-y-auto" data-testid={testId}>
      {children}
    </div>
  );
}

function NavButton({ label, onClick, children }: {
  label: string;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      onClick={onClick}
      className="text-muted-foreground hover:bg-accent hover:text-foreground flex size-6 items-center justify-center rounded transition-colors"
    >
      {children}
    </button>
  );
}

/** Weekday labels: Mon..Sun in zh (Monday start); narrow weekday labels in en (Sunday start). */
function buildWeekLabels(t: (key: string) => string, zh: boolean): string[] {
  if (zh) {
    return [
      t("dateTimePicker.weekdays.mon"),
      t("dateTimePicker.weekdays.tue"),
      t("dateTimePicker.weekdays.wed"),
      t("dateTimePicker.weekdays.thu"),
      t("dateTimePicker.weekdays.fri"),
      t("dateTimePicker.weekdays.sat"),
      t("dateTimePicker.weekdays.sun"),
    ];
  }
  return [
    t("dateTimePicker.weekdays.sun"),
    t("dateTimePicker.weekdays.mon"),
    t("dateTimePicker.weekdays.tue"),
    t("dateTimePicker.weekdays.wed"),
    t("dateTimePicker.weekdays.thu"),
    t("dateTimePicker.weekdays.fri"),
    t("dateTimePicker.weekdays.sat"),
  ];
}

/** 6x7 month grid (leading/trailing month padding cells are null; start day follows locale). */
function buildMonthCells(year: number, month: number, zh: boolean): Array<{ year: number; month: number; day: number } | null> {
  const firstOfMonth = new Date(year, month, 1);
  // zh starts on Monday (getDay Sunday=0 -> offset 6); en starts on Sunday (offset 0).
  const lead = zh ? (firstOfMonth.getDay() + 6) % 7 : firstOfMonth.getDay();
  const daysInMonth = new Date(year, month + 1, 0).getDate();
  const cells: Array<{ year: number; month: number; day: number } | null> = [];
  for (let i = 0; i < lead; i++) cells.push(null);
  for (let day = 1; day <= daysInMonth; day++) cells.push({ year, month, day });
  while (cells.length % 7 !== 0) cells.push(null);
  return cells;
}

function pad2(value: number): string {
  return String(value).padStart(2, "0");
}

function toDayKey(date: Date): string {
  return `${String(date.getFullYear())}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())}`;
}

function toTimeKey(date: Date): string {
  return `${pad2(date.getHours())}:${pad2(date.getMinutes())}`;
}

function parseValue(value: string): Date | null {
  const parsed = new Date(`${value.slice(0, 10)}T${value.slice(11, 16)}:00`);
  return Number.isNaN(parsed.getTime()) ? null : parsed;
}
