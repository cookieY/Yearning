import { createContext, useContext, useEffect, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getCurrentUser, logout as logoutRequest } from "@/api/generated/client/authentication/authentication";
import type { CurrentUser } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { TransportError, SESSION_EXPIRED_EVENT, unwrapApi } from "@/shared/api/mutator";
import { setSystemTimezone, useSystemTimezone } from "@/shared/lib/timezones";

/**
 * The session context is the only consumer of GET /users/me. Authentication
 * is carried by the HttpOnly session cookie, so "who am I" is a server
 * question: a 401 (transport path) means anonymous, while any other failure
 * (network, 5xx, malformed envelope) is a degraded "unavailable" state that
 * stays retryable instead of pretending the user signed out. The
 * session-expired event from the mutator keeps a mounted app in sync when a
 * background request discovers the expiry first.
 */

const SESSION_QUERY_KEY = ["auth", "session"] as const;

export type SessionStatus = "loading" | "authenticated" | "anonymous" | "unavailable";

interface SessionContextValue {
  status: SessionStatus;
  user: CurrentUser | null;
  /** The raw /users/me failure while degraded — for the transport error panel. */
  probeError: unknown;
  logout: () => Promise<void>;
  /** Retries the /users/me probe after a degraded state. */
  retry: () => void;
}

const SessionContext = createContext<SessionContextValue | null>(null);

function isAuthenticationError(error: unknown): boolean {
  return error instanceof TransportError && error.problem.status === 401;
}

function isSessionQueryKey(queryKey: readonly unknown[]): boolean {
  return (
    queryKey.length === SESSION_QUERY_KEY.length &&
    queryKey[0] === SESSION_QUERY_KEY[0] &&
    queryKey[1] === SESSION_QUERY_KEY[1]
  );
}

export function SessionProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  useSystemTimezone();

  const sessionQuery = useQuery<CurrentUser | null>({
    queryKey: SESSION_QUERY_KEY,
    queryFn: async () => {
      const current = await unwrapApi(getCurrentUser());
      if (typeof current.system_timezone === "string" && current.system_timezone.trim() !== "") {
        setSystemTimezone(current.system_timezone);
      }
      return current;
    },
    retry: false,
    staleTime: 60_000,
  });

  if (typeof sessionQuery.data?.system_timezone === "string" && sessionQuery.data.system_timezone.trim() !== "") {
    setSystemTimezone(sessionQuery.data.system_timezone);
  }

  useEffect(() => {
    // The mutator announces any 401 once; the provider flips the session to
    // anonymous and drops every per-user cache so a next login on the same
    // tab cannot observe the previous user's data. Deliberately no
    // invalidation of the session query itself — a refetch would 401 again
    // and loop forever while anonymous.
    const markExpired = () => {
      void queryClient.cancelQueries({ queryKey: SESSION_QUERY_KEY });
      queryClient.removeQueries({
        predicate: (query) => !isSessionQueryKey(query.queryKey),
      });
      queryClient.setQueryData<CurrentUser | null>(SESSION_QUERY_KEY, null);
    };
    window.addEventListener(SESSION_EXPIRED_EVENT, markExpired);
    return () => {
      window.removeEventListener(SESSION_EXPIRED_EVENT, markExpired);
    };
  }, [queryClient]);

  const logoutMutation = useMutation({
    mutationFn: () => logoutRequest(),
    onSettled: () => {
      // A successful logout leaves no session to keep cached: every per-user
      // query is dropped, and the session query itself is parked at null so
      // the active observers flip to anonymous without a refetch loop.
      queryClient.removeQueries({
        predicate: (query) => !isSessionQueryKey(query.queryKey),
      });
      queryClient.setQueryData<CurrentUser | null>(SESSION_QUERY_KEY, null);
    },
  });

  const logout = async (): Promise<void> => {
    await logoutMutation.mutateAsync();
  };

  const retry = () => {
    void sessionQuery.refetch();
  };

  let status: SessionStatus;
  if (sessionQuery.isPending) {
    status = "loading";
  } else if (sessionQuery.isError) {
    status = isAuthenticationError(sessionQuery.error) ? "anonymous" : "unavailable";
  } else {
    status = sessionQuery.data === null ? "anonymous" : "authenticated";
  }
  const user = status === "authenticated" ? (sessionQuery.data ?? null) : null;
  const probeError = status === "unavailable" ? sessionQuery.error : null;
  const value: SessionContextValue = { status, user, probeError, logout, retry };

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionContextValue {
  const context = useContext(SessionContext);
  if (context === null) {
    throw new Error("useSession must be used within SessionProvider");
  }
  return context;
}

export { SESSION_QUERY_KEY };
