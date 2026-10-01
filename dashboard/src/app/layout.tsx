import type { Metadata, Viewport } from 'next';
import { Inter, JetBrains_Mono } from 'next/font/google';

import { Shell } from '@/components/layout/shell';

import './globals.css';
import { Providers } from './providers';

/**
 * Inter carries the whole product voice: UI text and tabular figures. It is
 * bundled at build time, so the dashboard renders identically offline and
 * behind air-gapped proxies. JetBrains Mono handles ids, hashes and code —
 * monospace is reserved for data, never used as decoration.
 */
const inter = Inter({
  subsets: ['latin'],
  display: 'swap',
  variable: '--font-sans',
});

const jetBrainsMono = JetBrains_Mono({
  subsets: ['latin'],
  display: 'swap',
  variable: '--font-mono',
});

export const metadata: Metadata = {
  title: {
    default: 'CoreRouter',
    template: '%s · CoreRouter',
  },
  description: 'CoreRouter control plane: traffic, routing, providers and spend.',
  robots: { index: false, follow: false },
};

export const viewport: Viewport = {
  width: 'device-width',
  initialScale: 1,
  themeColor: [
    { media: '(prefers-color-scheme: light)', color: '#ffffff' },
    { media: '(prefers-color-scheme: dark)', color: '#0b1220' },
  ],
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className={`dark ${inter.variable} ${jetBrainsMono.variable}`}>
      <body className="font-sans">
        <Providers>
          <Shell>{children}</Shell>
        </Providers>
      </body>
    </html>
  );
}
