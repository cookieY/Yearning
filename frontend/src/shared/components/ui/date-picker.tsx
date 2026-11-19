import { useTranslation } from "react-i18next";
import { CalendarIcon, Clock2Icon } from "lucide-react";
import type { Locale } from "react-day-picker";
import { enUS, zhCN } from "react-day-picker/locale";

import { Button } from "@/shared/components/ui/button";
import { Calendar } from "@/shared/components/ui/calendar";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/shared/components/ui/input-group";
import { Label } from "@/shared/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/shared/components/ui/popover";
import { cn } from "@/shared/lib/utils";

/** Local-date helpers: filter values are plain YYYY-MM-DD strings; constructing
 * through the local clock keeps the picked day stable across timezones. */
function parseLocalDate(value: string): Date {
  const [year, month, day] = value.split("-").map(Number);
  return new Date(year ?? 1970, (month ?? 1) - 1, day ?? 1);
}

function formatLocalDate(date: Date): string {
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${String(date.getFullYear())}-${month}-${day}`;
}

/**
 * shadcn DatePicker (Popover + Calendar, plus optional embedded shadcn
 * InputGroup time footer per shadcn v4 calendar-time composition). Controlled
 * by a plain YYYY-MM-DD string ("" = no selection) and optional HH:mm time.
 */
export function DatePicker({
  value,
  onChange,
  time,
  onTimeChange,
  timeLabel,
  timeId,
  timeTestId,
  placeholder,
  ariaLabel,
  id,
  testId,
  className,
  disableBefore,
}: {
  value: string;
  onChange: (next: string) => void;
  /** Optional HH:mm wall-clock time combined inside the same DatePicker popover. */
  time?: string;
  onTimeChange?: (next: string) => void;
  timeLabel?: string;
  timeId?: string;
  timeTestId?: string;
  placeholder?: string;
  ariaLabel?: string;
  id?: string;
  testId?: string;
  className?: string;
  /** Plain YYYY-MM-DD; days strictly before this date are not selectable (UX-006). */
  disableBefore?: string;
}) {
  const { i18n } = useTranslation();
  const locale: Locale = i18n.language.startsWith("zh") ? zhCN : enUS;
  const selected = value === "" ? undefined : parseLocalDate(value);
  const disabledDays =
    disableBefore === undefined ? undefined : { before: parseLocalDate(disableBefore) };
  const formattedDate = selected === undefined ? "" : formatLocalDate(selected);
  const displayText =
    formattedDate === ""
      ? (placeholder ?? "--/--/----")
      : time !== undefined && time !== ""
        ? `${formattedDate} ${time}`
        : formattedDate;

  return (
    <Popover>
      <PopoverTrigger
        render={
          <Button
            id={id ?? testId}
            type="button"
            variant="outline"
            aria-label={ariaLabel}
            data-testid={testId}
            className={cn(
              "h-8 w-36 justify-start gap-2 px-2.5 text-left text-xs font-normal",
              !value && "text-muted-foreground",
              className,
            )}
          >
            <CalendarIcon className="size-4" aria-hidden />
            {displayText}
          </Button>
        }
      />
      <PopoverContent className="w-auto p-0" align="start">
        <Calendar
          mode="single"
          defaultMonth={selected}
          selected={selected}
          disabled={disabledDays}
          onSelect={(day) => {
            onChange(day === undefined ? "" : formatLocalDate(day));
          }}
          locale={locale}
        />
        {time !== undefined && onTimeChange !== undefined && (
          <div className="flex flex-col gap-1.5 border-t border-border p-3">
            {timeLabel !== undefined && (
              <Label htmlFor={timeId} className="text-xs font-medium text-muted-foreground">
                {timeLabel}
              </Label>
            )}
            <InputGroup>
              <InputGroupInput
                id={timeId}
                type="time"
                value={time}
                onChange={(event) => {
                  onTimeChange(event.target.value);
                }}
                className="appearance-none [&::-webkit-calendar-picker-indicator]:hidden [&::-webkit-calendar-picker-indicator]:appearance-none"
                data-testid={timeTestId}
              />
              <InputGroupAddon align="inline-end">
                <Clock2Icon className="text-muted-foreground" aria-hidden />
              </InputGroupAddon>
            </InputGroup>
          </div>
        )}
      </PopoverContent>
    </Popover>
  );
}
