import { useRef } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";

describe("DialogContent initialFocus", () => {
  it("focuses the popup container by default so ScrollArea stays at scrollTop 0", async () => {
    render(
      <Dialog open>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Tall Dialog</DialogTitle>
            <DialogDescription>Dialog body description</DialogDescription>
          </DialogHeader>
          <div style={{ height: 600 }}>Static top notice</div>
          <input aria-label="Nested input" />
        </DialogContent>
      </Dialog>,
    );

    const dialog = await screen.findByRole("dialog");
    const viewport = document.querySelector('[data-slot="scroll-area-viewport"]');
    expect(viewport).not.toBeNull();
    expect(viewport?.scrollTop).toBe(0);

    await waitFor(() => {
      expect(dialog).toHaveFocus();
    });
    expect(screen.getByLabelText("Nested input")).not.toHaveFocus();
    expect(viewport?.scrollTop).toBe(0);
  });

  it("still honors an explicit initialFocus ref override", async () => {
    function ExplicitFocusDialog() {
      const inputRef = useRef<HTMLInputElement>(null);
      return (
        <Dialog open>
          <DialogContent initialFocus={inputRef}>
            <DialogHeader>
              <DialogTitle>Explicit Focus</DialogTitle>
              <DialogDescription>Targets ref</DialogDescription>
            </DialogHeader>
            <input ref={inputRef} aria-label="Targeted input" />
          </DialogContent>
        </Dialog>
      );
    }

    render(<ExplicitFocusDialog />);
    await waitFor(() => {
      expect(screen.getByLabelText("Targeted input")).toHaveFocus();
    });
  });

  it("preserves autoFocus on child inputs", async () => {
    render(
      <Dialog open>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>AutoFocus Dialog</DialogTitle>
            <DialogDescription>Command search style</DialogDescription>
          </DialogHeader>
          {/* eslint-disable-next-line jsx-a11y/no-autofocus */}
          <input autoFocus aria-label="Command input" />
        </DialogContent>
      </Dialog>,
    );

    await waitFor(() => {
      expect(screen.getByLabelText("Command input")).toHaveFocus();
    });
  });
});
