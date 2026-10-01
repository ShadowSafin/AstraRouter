import { Suspense } from 'react';

import { RequestsView } from '@/components/views/requests-view';
import { TableSkeleton } from '@/components/ui/state';

export const metadata = { title: 'Requests' };

export default function RequestsPage() {
  return (
    <Suspense fallback={<TableSkeleton rows={10} columns={8} />}>
      <RequestsView />
    </Suspense>
  );
}
