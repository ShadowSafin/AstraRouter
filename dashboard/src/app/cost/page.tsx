import { Suspense } from 'react';

import { CardsSkeleton } from '@/components/ui/state';
import { CostView } from '@/components/views/cost-view';

export const metadata = { title: 'Cost intelligence' };

export default function CostPage() {
  return (
    <Suspense fallback={<CardsSkeleton count={4} />}>
      <CostView />
    </Suspense>
  );
}
