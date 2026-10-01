import { Suspense } from 'react';

import { KeysView } from '@/components/views/keys-view';
import { TableSkeleton } from '@/components/ui/state';

export const metadata = { title: 'API keys' };

export default function KeysPage() {
  return (
    <Suspense fallback={<TableSkeleton rows={4} columns={6} />}>
      <KeysView />
    </Suspense>
  );
}
