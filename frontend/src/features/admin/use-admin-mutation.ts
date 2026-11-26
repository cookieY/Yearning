import { useMutation, useQueryClient } from "@tanstack/react-query";

/**
 * Shared pagination helpers and mutation wrapper for the admin server-state
 * hooks. Every admin mutation carries If-Match (row version) where applicable
 * and invalidates its corresponding list/read query keys directly.
 */

export const PAGE_LIMIT = 200;

export function pageItems<T>(data: { items?: T[] } | undefined | null): T[] {
  return data?.items ?? [];
}

type QueryKeyList = ReadonlyArray<readonly unknown[]>;

export type ResourceMutationOptions<TData, TVariables> =
  | {
      mutationFn: (variables: TVariables) => Promise<TData>;
      invalidateKeys: QueryKeyList | ((data: TData, variables: TVariables) => QueryKeyList);
      when?: "success";
      afterSuccess?: (data: TData, variables: TVariables) => void;
    }
  | {
      mutationFn: (variables: TVariables) => Promise<TData>;
      invalidateKeys:
        | QueryKeyList
        | ((data: TData | undefined, variables: TVariables) => QueryKeyList);
      when: "settled";
      afterSuccess?: (data: TData, variables: TVariables) => void;
    };

export function useResourceMutation<TData, TVariables>(
  options: ResourceMutationOptions<TData, TVariables>,
) {
  const queryClient = useQueryClient();
  const invalidateAll = (keys: QueryKeyList) => {
    for (const queryKey of keys) {
      void queryClient.invalidateQueries({ queryKey: [...queryKey] });
    }
  };
  return useMutation<TData, Error, TVariables>({
    mutationFn: options.mutationFn,
    onSuccess: (data, variables) => {
      options.afterSuccess?.(data, variables);
      if (options.when !== "settled") {
        invalidateAll(
          typeof options.invalidateKeys === "function"
            ? options.invalidateKeys(data, variables)
            : options.invalidateKeys,
        );
      }
    },
    onSettled: (data, _error, variables) => {
      if (options.when === "settled") {
        invalidateAll(
          typeof options.invalidateKeys === "function"
            ? options.invalidateKeys(data, variables)
            : options.invalidateKeys,
        );
      }
    },
  });
}
