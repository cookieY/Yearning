import { Component, type ErrorInfo, type ReactNode } from "react";
import i18next from "@/shared/i18n";

interface ErrorBoundaryProps {
  children: ReactNode;
}

interface ErrorBoundaryState {
  error: Error | null;
}

/**
 * Last-resort render guard. Typed errors remain available to feature UIs;
 * this boundary only prevents a blank screen and keeps a safe generic message
 * without leaking internals.
 */
export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    console.error("unhandled render error", error, info.componentStack);
  }

  render(): ReactNode {
    if (this.state.error) {
      return (
        <div role="alert" className="flex min-h-screen items-center justify-center p-6">
          <div className="max-w-md text-center">
            <h1 className="text-lg font-semibold">{i18next.t("errors.boundaryTitle")}</h1>
            <p className="mt-2 text-sm text-muted-foreground">
              {i18next.t("errors.boundaryDesc")}
            </p>
            <button
              type="button"
              className="mt-4 rounded-md bg-primary px-4 py-2 text-sm text-primary-foreground"
              onClick={() => {
                window.location.reload();
              }}
            >
              {i18next.t("errors.reload")}
            </button>
          </div>
        </div>
      );
    }
    return this.props.children;
  }
}
