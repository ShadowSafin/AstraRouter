import { Suspense } from 'react';

import { AnalyticsView } from '@/components/views/analytics-view';
import { CardsSkeleton } from '@/components/ui/state';

export const metadata = { title: 'Analytics' };

export default function AnalyticsPage() {
  return (
    <Suspense fallback={<CardsSkeleton count={4} />}>
      <AnalyticsView />
    </Suspense>
  );
}
