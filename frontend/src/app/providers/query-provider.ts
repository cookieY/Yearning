import {
  QueryClient,
  QueryCache,
  MutationCache,
} from "@tanstack/react-query";
import { BusinessError, TransportError } from "@/shared/api/mutator";

/**
 * Server state lives in TanStack Query only (frontend implementation PRD §4);
 * no second request or cache layer is introduced. Transport errors surface
 * through the shared error path; business errors carry their typed err_code
 * for feature-level handling.
 */
export function createQueryClient(): QueryClient {
  return new QueryClient({
    queryCache: new QueryCache({
      onError: (error) => {
        if (error instanceof TransportError) {
          // Shared transport failure surface; request_id is safe to expose.
          console.error(`request failed: ${error.problem.title} (${String(error.problem.status)})`);
        }
      },
    }),
    mutationCache: new MutationCache({
      onError: (error) => {
        if (error instanceof BusinessError && error.retryable) {
          // Retryable business failures are surfaced to the originating UI in
          // later packages; the skeleton only guarantees typed propagation.
          console.debug(`retryable business error ${String(error.err_code)}`);
        }
      },
    }),
    defaultOptions: {
      queries: {
        retry: (failureCount, error) => {
          if (error instanceof BusinessError) return false;
          if (error instanceof TransportError && error.problem.status >= 400 && error.problem.status < 500) {
            return false;
          }
          return failureCount < 2;
        },
        staleTime: 15_000,
        refetchOnWindowFocus: false,
      },
    },
  });
}
