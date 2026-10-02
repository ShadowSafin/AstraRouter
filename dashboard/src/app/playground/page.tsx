import { Suspense } from 'react';

import { TableSkeleton } from '@/components/ui/state';
import { PlaygroundView } from '@/components/views/playground-view';

export const metadata = { title: 'Playground' };

export default function PlaygroundPage() {
  return (
    <Suspense fallback={<TableSkeleton rows={6} columns={3} />}>
      <PlaygroundView />
    </Suspense>
  );
}
