'use client';

import {
  Activity,
  Bot,
  Boxes,
  ChartColumn,
  ChevronDown,
  CircleDollarSign,
  Cpu,
  DatabaseZap,
  FlaskConical,
  Gauge,
  Globe,
  History,
  ListTree,
  Lock,
  Network,
  OctagonX,
  Play,
  ScrollText,
  Settings,
  ShieldAlert,
  Trophy,
  Users,
  BookKey,
  Wrench,
  Zap,
} from 'lucide-react';
import Link from 'next/link';
import { usePathname } from 'next/navigation';
import * as React from 'react';

import { useProviderHealth } from '@/hooks/use-admin';
import { cn } from '@/lib/utils';

export function SidebarBrand({ compact = false }: { compact?: boolean }) {
  return (
    <span className="flex items-center gap-2.5">
      <span className="flex size-8 items-center justify-center rounded-lg bg-primary text-primary-foreground">
        <Zap className="size-4" strokeWidth={2.5} />
      </span>
      <span className={cn('leading-tight', compact && 'sr-only min-[400px]:not-sr-only')}>
        <span className="block text-sm font-semibold tracking-tight">CoreRouter</span>
        <span className="block text-[11px] text-muted-foreground">Control plane</span>
      </span>
    </span>
  );
}

interface NavItem {
  href: string;
  label: string;
  icon: React.ComponentType<{ className?: string; strokeWidth?: number | string }>;
  /** A one-line hint shown on hover, for the pages whose purpose is not obvious. */
  hint: string;
  /** A small count shown at the row's end; hidden when zero. */
  badge?: number;
  /** The badge marks something needing attention rather than a plain count. */
  badgeAlert?: boolean;
}

function NavRow({ item, active }: { item: NavItem; active: boolean }) {
  const Icon = item.icon;
  return (
    <Link
      href={item.href}
      title={item.hint}
      aria-current={active ? 'page' : undefined}
      className={cn(
        'group flex items-center gap-2.5 rounded-lg px-2.5 py-[7px] text-[13px] transition-colors',
        active
          ? 'bg-accent font-medium text-foreground'
          : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground',
      )}
    >
      <Icon
        className={cn('size-[17px] shrink-0', active ? 'text-primary' : 'text-muted-foreground group-hover:text-foreground')}
        strokeWidth={active ? 2.25 : 2}
      />
      <span className="min-w-0 flex-1 truncate">{item.label}</span>
      {item.badge != null && item.badge > 0 ? (
        <span
          className={cn(
            'rounded-full px-1.5 py-px text-[11px] font-medium tabular-nums',
            item.badgeAlert ? 'bg-danger/15 text-danger' : 'bg-muted text-muted-foreground',
          )}
        >
          {item.badge}
        </span>
      ) : null}
    </Link>
  );
}

const SECTIONS: Array<{ title: string; items: NavItem[] }> = [
  {
    title: 'Workspace',
    items: [
      { href: '/', label: 'Dashboard', icon: Gauge, hint: 'Traffic, latency, cost and health at a glance' },
      { href: '/analytics', label: 'Analytics', icon: ChartColumn, hint: 'Traffic volume, latency, cache and spend' },
      { href: '/requests', label: 'Requests', icon: ScrollText, hint: 'Per-request log with routing detail' },
    ],
  },
  {
    title: 'Inference',
    items: [
      { href: '/providers', label: 'Providers', icon: Cpu, hint: 'Upstream endpoints and their health' },
      { href: '/models', label: 'Models', icon: Boxes, hint: 'The servable model registry' },
    ],
  },
  {
    title: 'Routing',
    items: [
      { href: '/policies', label: 'Policies', icon: ListTree, hint: 'How requests choose a provider' },
      { href: '/endpoints', label: 'Endpoints', icon: Network, hint: 'Per-endpoint routing overrides' },
    ],
  },
  {
    title: 'Tools',
    items: [
      { href: '/tools', label: 'Tool registry', icon: Wrench, hint: 'Built-in and external tools, and their safety' },
      { href: '/agent-runs', label: 'Agent runs', icon: Bot, hint: 'Bounded multi-step runs and their step traces' },
    ],
  },
  {
    title: 'Access',
    items: [
      { href: '/keys', label: 'API keys', icon: BookKey, hint: 'Credentials and their scopes' },
      { href: '/tenants', label: 'Tenants', icon: Users, hint: 'Ownership boundaries' },
    ],
  },
  {
    title: 'System',
    items: [
      { href: '/tunnels', label: 'Tunnels', icon: Globe, hint: 'Disposable public URLs for remote access' },
      { href: '/audit', label: 'Audit log', icon: History, hint: 'Control-plane change history' },
      { href: '/settings', label: 'Settings', icon: Settings, hint: 'Build, runtime and dependency status' },
    ],
  },
];

const MORE_ITEMS: NavItem[] = [
  { href: '/errors', label: 'Errors', icon: ShieldAlert, hint: 'Failures grouped by error code' },
  { href: '/budgets', label: 'Budgets', icon: CircleDollarSign, hint: 'Spend limits and current consumption' },
  { href: '/scores', label: 'Scores', icon: Trophy, hint: 'Explainable provider and model rankings' },
  { href: '/overrides', label: 'Overrides', icon: OctagonX, hint: 'Manual kill switches and forced states' },
  { href: '/cache', label: 'Cache', icon: DatabaseZap, hint: 'Exact, prefix and semantic hit rates' },
  { href: '/replay', label: 'Replay', icon: Play, hint: 'Re-execute captured traffic offline' },
  { href: '/evaluations', label: 'Evaluations', icon: FlaskConical, hint: 'Quality, latency and cost comparisons' },
  { href: '/tool-policies', label: 'Tool policies', icon: Lock, hint: 'Who may run what, how far, and how long' },
];

/**
 * The left rail: brand, curated sections, an overflow group, and the operator.
 *
 * Twelve routes stay visible; the remaining eight live under More, expanded
 * automatically when the current page is one of them. The only live number in
 * the rail is the unhealthy-provider count — a rail full of counters is a rail
 * nobody reads. The operator block states the access truthfully: there is no
 * user identity in this product, only the admin key.
 */
export function Sidebar({ onNavigate, inDrawer = false }: { onNavigate?: () => void; inDrawer?: boolean }) {
  const pathname = usePathname();
  const { data: health } = useProviderHealth();
  const [moreOpen, setMoreOpen] = React.useState(() => MORE_ITEMS.some((item) => item.href === pathname));

  React.useEffect(() => {
    if (MORE_ITEMS.some((item) => item.href === pathname)) setMoreOpen(true);
  }, [pathname]);

  const unhealthy = (health ?? []).filter((h) => h.state === 'unhealthy' || h.state === 'degraded').length;

  const sections = React.useMemo(
    () =>
      SECTIONS.map((section) => ({
        ...section,
        items: section.items.map((item) =>
          item.href === '/providers' && unhealthy > 0 ? { ...item, badge: unhealthy, badgeAlert: true } : item,
        ),
      })),
    [unhealthy],
  );

  const isActive = (href: string) => pathname === href;

  return (
    <aside
      className={cn(
        'flex w-60 shrink-0 flex-col bg-transparent',
        inDrawer ? 'h-[calc(100dvh-3.5rem)]' : 'sticky top-0 h-screen',
      )}
    >
      {!inDrawer ? (
        <div className="flex h-16 items-center px-4">
          <SidebarBrand />
        </div>
      ) : null}

      <nav aria-label="Primary" className="flex-1 overflow-y-auto px-3 py-3">
        {sections.map((section) => (
          <div key={section.title} className="mb-4">
            <p className="px-2.5 pb-1.5 text-[10px] font-semibold uppercase tracking-[0.08em] text-muted-foreground/80">
              {section.title}
            </p>
            <ul className="space-y-0.5">
              {section.items.map((item) => (
                <li key={item.href}>
                  <span onClick={onNavigate}>
                    <NavRow item={item} active={isActive(item.href)} />
                  </span>
                </li>
              ))}
            </ul>
          </div>
        ))}

        <div className="mb-2">
          <button
            type="button"
            onClick={() => setMoreOpen((open) => !open)}
            aria-expanded={moreOpen}
            className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-[7px] text-[13px] text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
          >
            <Activity className="size-[17px] shrink-0" strokeWidth={2} />
            <span className="flex-1 text-left">More</span>
            <ChevronDown className={cn('size-3.5 transition-transform', moreOpen && 'rotate-180')} />
          </button>
          {moreOpen ? (
            <ul className="mt-0.5 space-y-0.5">
              {MORE_ITEMS.map((item) => (
                <li key={item.href}>
                  <span onClick={onNavigate}>
                    <NavRow item={item} active={isActive(item.href)} />
                  </span>
                </li>
              ))}
            </ul>
          ) : null}
        </div>
      </nav>

      <div className="border-t border-border p-3">
        <Link
          href="/settings"
          onClick={onNavigate}
          title="Operator settings"
          className="flex items-center gap-2.5 rounded-lg px-2 py-1.5 transition-colors hover:bg-accent/60"
        >
          <span className="flex size-8 shrink-0 items-center justify-center rounded-full bg-muted text-xs font-semibold text-foreground">
            OP
          </span>
          <span className="min-w-0 leading-tight">
            <span className="block truncate text-[13px] font-medium">Operator</span>
            <span className="block truncate text-[11px] text-muted-foreground">Admin key access</span>
          </span>
        </Link>
      </div>
    </aside>
  );
}
