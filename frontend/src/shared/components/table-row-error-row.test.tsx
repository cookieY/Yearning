import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import i18n from "@/shared/i18n";
import { Table, TableBody } from "@/shared/components/ui/table";
import { TableRowErrorRow } from "./table-row-error-row";

function renderErrorRow(props: {
  colSpan?: number;
  summary?: string;
  detail?: string | null;
  onClose?: () => void;
}) {
  return render(
    <Table>
      <TableBody>
        <TableRowErrorRow
          colSpan={props.colSpan ?? 6}
          summary={props.summary ?? "Target datasource unavailable."}
          detail={props.detail}
          onClose={props.onClose}
          testId="row-err"
          detailTestId="row-err-detail"
          copyTestId="row-err-copy"
          closeTestId="row-err-close"
        />
      </TableBody>
    </Table>,
  );
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("TableRowErrorRow", () => {
  it("renders a full-width wrapping sub-row with summary and monospace detail", () => {
    const detail =
      "datasource: capability target unsupported: engine mysql mode mysql detected version 8.4.13 is not in the frozen capability matrix";
    renderErrorRow({
      colSpan: 6,
      summary: "Target datasource unavailable.",
      detail,
      onClose: vi.fn(),
    });

    const row = screen.getByTestId("row-err");
    const cell = row.querySelector("td");
    expect(cell).not.toBeNull();
    expect(cell).toHaveAttribute("colspan", "6");
    expect(cell).toHaveClass("whitespace-normal");

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("Target datasource unavailable.");
    expect(screen.getByTestId("row-err-detail")).toHaveTextContent(detail);
  });

  it("copies summary and detail together and updates copied feedback", async () => {
    const user = userEvent.setup();
    const writeSpy = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    const detail = "dial tcp 127.0.0.1:3306: connect: connection refused";

    renderErrorRow({
      summary: "Target datasource unavailable.",
      detail,
    });

    const copyBtn = screen.getByTestId("row-err-copy");
    expect(copyBtn).toHaveTextContent(i18n.t("common.copy"));

    await user.click(copyBtn);

    expect(writeSpy).toHaveBeenCalledWith(`Target datasource unavailable.\n${detail}`);
    await waitFor(() => {
      expect(copyBtn).toHaveTextContent(i18n.t("common.copied"));
    });
  });

  it("copies only summary when detail is omitted or whitespace-only", async () => {
    const user = userEvent.setup();
    const writeSpy = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);

    renderErrorRow({
      summary: "Connection test failed.",
      detail: "   ",
    });

    expect(screen.queryByTestId("row-err-detail")).toBeNull();
    await user.click(screen.getByTestId("row-err-copy"));
    expect(writeSpy).toHaveBeenCalledWith("Connection test failed.");
  });

  it("handles clipboard rejection without flipping to copied state", async () => {
    const user = userEvent.setup();
    vi.spyOn(navigator.clipboard, "writeText").mockRejectedValue(new Error("denied"));

    renderErrorRow({
      summary: "Connection test failed.",
      detail: "permission denied",
    });

    const copyBtn = screen.getByTestId("row-err-copy");
    await user.click(copyBtn);

    await waitFor(() => {
      expect(copyBtn).toHaveTextContent(i18n.t("common.copy"));
    });
  });

  it("invokes onClose when the close button is clicked and hides it when omitted", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();

    const { rerender } = renderErrorRow({
      summary: "Connection test failed.",
      onClose,
    });

    await user.click(screen.getByTestId("row-err-close"));
    expect(onClose).toHaveBeenCalledTimes(1);

    rerender(
      <Table>
        <TableBody>
          <TableRowErrorRow
            colSpan={6}
            summary="Connection test failed."
            testId="row-err"
            copyTestId="row-err-copy"
            closeTestId="row-err-close"
          />
        </TableBody>
      </Table>,
    );
    expect(screen.queryByTestId("row-err-close")).toBeNull();
  });
});
