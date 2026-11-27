import { Suspense, type LazyExoticComponent, type ComponentType } from "react";
import { PageLoadingState } from "@/shared/components/status/page-loading-state";

let nextLoadableId = 0;

/** Route chunks use the same accessible page skeleton as data reads. */
export function Loadable(component: LazyExoticComponent<ComponentType>) {
  const Component = component;
  const suspenseKey = `loadable-${String(++nextLoadableId)}`;
  return (
    <Suspense
      key={suspenseKey}
      fallback={<PageLoadingState />}
    >
      <Component />
    </Suspense>
  );
}
