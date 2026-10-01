import { Suspense } from 'react';

import { OverviewView } from '@/components/views/overview-view';
import { CardsSkeleton } from '@/components/ui/state';

// The view reads the window and tenant from the query string, so it must render
// inside a Suspense boundary for the static shell to be produced.
export default function OverviewPage() {
  return (
    <Suspense fallback={<CardsSkeleton count={6} />}>
      <OverviewView />
    </Suspense>
  );
}
