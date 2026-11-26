import { useRef } from "react";
import {
  useMutation,
  type DefaultError,
  type MutationFunctionContext,
  type UseMutationOptions,
  type UseMutationResult,
} from "@tanstack/react-query";
import { newIdempotencyKey } from "@/shared/api/mutator";

export type IdempotentMutationOptions<
  TData = unknown,
  TError = DefaultError,
  TVariables = void,
  TContext = unknown,
> = Omit<UseMutationOptions<TData, TError, TVariables, TContext>, "mutationFn"> & {
  mutationFn: (variables: TVariables, idempotencyKey: string) => Promise<TData>;
};

/**
 * Wraps `useMutation` so each distinct `mutate` / `mutateAsync` invocation binds
 * one `Idempotency-Key` to TanStack Query's per-invocation `MutationFunctionContext`
 * (via `WeakMap`), reusing that exact key across retries of the same invocation
 * while isolating overlapping concurrent invocations and minting a fresh key on
 * every new user action.
 */
export function useIdempotentMutation<
  TData = unknown,
  TError = DefaultError,
  TVariables = void,
  TContext = unknown,
>(
  prefix: string | undefined,
  options: IdempotentMutationOptions<TData, TError, TVariables, TContext>,
): UseMutationResult<TData, TError, TVariables, TContext> {
  const keysByContextRef = useRef(new WeakMap<object, string>());
  return useMutation({
    ...options,
    mutationFn: (variables: TVariables, context: MutationFunctionContext) => {
      let key = keysByContextRef.current.get(context);
      if (key === undefined) {
        key = newIdempotencyKey(prefix);
        keysByContextRef.current.set(context, key);
      }
      return options.mutationFn(variables, key);
    },
  });
}
