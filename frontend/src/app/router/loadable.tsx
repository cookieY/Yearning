import { Suspense, type LazyExoticComponent, type ComponentType } from "react";
import { PageLoadingState } from "@/shared/components/status/page-loading-state";

/** Route chunks use the same accessible page skeleton as data reads. */
export function Loadable(component: LazyExoticComponent<ComponentType>) {
  const Component = component;
  return (
    <Suspense
      fallback={<PageLoadingState />}
    >
      <Component />
    </Suspense>
  );
}
