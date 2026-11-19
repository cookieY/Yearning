import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

/**
 * Keyboard-open a TableRowActions kebab menu under jsdom.
 *
 * Base UI's Menu.Trigger does not open on user-event's synthetic pointer
 * sequence in jsdom (no window.PointerEvent → the pointer gestures are
 * ignored), but keyboard activation works: focus the trigger and press Enter.
 * The menu items render in a portal on document.body, so item lookups must be
 * screen-scoped — a row-scoped `within(row)` query cannot see them.
 *
 * Use the element variant when several rows exist (a screen-scoped regex
 * trigger lookup would be ambiguous); use the testId variant when the id is
 * exact and unique.
 */
export async function openRowActionsAt(trigger: HTMLElement): Promise<void> {
  trigger.focus();
  await userEvent.keyboard("{Enter}");
}

export async function openRowActions(testId: string | RegExp): Promise<void> {
  await openRowActionsAt(screen.getByTestId(testId));
}
