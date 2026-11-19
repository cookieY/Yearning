import type { ReactNode } from "react";
import { Inbox, SearchX } from "lucide-react";
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/shared/components/ui/empty";

export function EmptyState({ title, description, filtered = false, action, testId }: {
  title: ReactNode;
  description?: ReactNode;
  filtered?: boolean;
  action?: ReactNode;
  testId?: string;
}) {
  const Icon = filtered ? SearchX : Inbox;
  return (
    <Empty data-testid={testId}>
      <EmptyHeader>
        <EmptyMedia variant="icon"><Icon aria-hidden /></EmptyMedia>
        <EmptyTitle>{title}</EmptyTitle>
        {description && <EmptyDescription>{description}</EmptyDescription>}
      </EmptyHeader>
      {action && <EmptyContent>{action}</EmptyContent>}
    </Empty>
  );
}
