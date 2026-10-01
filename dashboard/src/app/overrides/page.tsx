import { Suspense } from 'react';

import { CardsSkeleton } from '@/components/ui/state';
import { OverridesView } from '@/components/views/overrides-view';

export const metadata = { title: 'Overrides' };

export default function OverridesPage() {
  return (
    <Suspense fallback={<CardsSkeleton count={3} />}>
      <OverridesView />
    </Suspense>
  );
}
