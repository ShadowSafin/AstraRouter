import { Suspense } from 'react';

import { BudgetsView } from '@/components/views/budgets-view';
import { TableSkeleton } from '@/components/ui/state';

export const metadata = { title: 'Budgets' };

export default function BudgetsPage() {
  return (
    <Suspense fallback={<TableSkeleton rows={3} columns={3} />}>
      <BudgetsView />
    </Suspense>
  );
}
