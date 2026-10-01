import { Suspense } from 'react';

import { ErrorsView } from '@/components/views/errors-view';
import { TableSkeleton } from '@/components/ui/state';

export const metadata = { title: 'Errors' };

export default function ErrorsPage() {
  return (
    <Suspense fallback={<TableSkeleton rows={8} columns={6} />}>
      <ErrorsView />
    </Suspense>
  );
}
