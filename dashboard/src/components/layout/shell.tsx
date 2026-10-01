'use client';

import { Menu, X } from 'lucide-react';
import * as React from 'react';

import { Sidebar, SidebarBrand } from './sidebar';

/**
 * The application shell: a full-screen black canvas with a fixed sidebar and
 * a scrolling content column.
 *
 * No boxed container, no floating panel — the rail and the content sit
 * directly on the page background, separated by a single hairline. On narrow
 * screens the rail becomes a drawer. The one authored motion in the product
 * lives here: the drawer slide. Everything else stays still.
 */
export function Shell({ children }: { children: React.ReactNode }) {
  const [navOpen, setNavOpen] = React.useState(false);
  const menuButtonRef = React.useRef<HTMLButtonElement>(null);

  const closeNav = React.useCallback(() => {
    setNavOpen(false);
    menuButtonRef.current?.focus();
  }, []);

  React.useEffect(() => {
    if (!navOpen) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') closeNav();
    };
    document.addEventListener('keydown', onKey);
    document.body.style.overflow = 'hidden';
    return () => {
      document.removeEventListener('keydown', onKey);
      document.body.style.overflow = '';
    };
  }, [navOpen, closeNav]);

  return (
    <div className="min-h-screen bg-background">
      {/* Mobile top bar. The sidebar owns navigation on desktop; this bar only
          exists to open it where the rail does not fit. */}
      <div className="sticky top-0 z-30 flex h-14 items-center gap-3 border-b border-border bg-background px-4 lg:hidden">
        <button
          ref={menuButtonRef}
          type="button"
          onClick={() => setNavOpen(true)}
          aria-label="Open navigation"
          aria-expanded={navOpen}
          className="flex size-9 items-center justify-center rounded-lg border border-border bg-card text-muted-foreground transition-colors hover:text-foreground"
        >
          <Menu className="size-4" />
        </button>
        <SidebarBrand compact />
      </div>

      <div className="flex min-h-screen lg:min-h-screen">
        {/* Desktop rail. */}
        <div className="hidden shrink-0 border-r border-border lg:block">
          <Sidebar />
        </div>

        {/* Mobile drawer. */}
        <div
          className={`fixed inset-0 z-40 lg:hidden ${navOpen ? '' : 'pointer-events-none'}`}
          aria-hidden={!navOpen}
        >
          <div
            className={`absolute inset-0 bg-black/70 transition-opacity duration-200 ease-out ${
              navOpen ? 'opacity-100' : 'opacity-0'
            }`}
            onClick={closeNav}
          />
          <div
            role="dialog"
            aria-modal="true"
            aria-label="Navigation"
            className={`absolute inset-y-0 left-0 w-[280px] border-r border-border bg-card transition-transform duration-200 ease-out ${
              navOpen ? 'translate-x-0' : '-translate-x-full'
            }`}
          >
            <div className="flex h-14 items-center justify-between border-b border-border px-4">
              <SidebarBrand compact />
              <button
                type="button"
                onClick={closeNav}
                aria-label="Close navigation"
                className="flex size-9 items-center justify-center rounded-lg border border-border text-muted-foreground transition-colors hover:text-foreground"
              >
                <X className="size-4" />
              </button>
            </div>
            <Sidebar onNavigate={closeNav} inDrawer />
          </div>
        </div>

        <div className="flex min-w-0 flex-1 flex-col">
          <main className="mx-auto w-full max-w-[1500px] flex-1 px-4 py-6 sm:px-6 sm:py-8 lg:px-10">
            {children}
          </main>
          <footer className="border-t border-border px-6 py-3 text-center text-[11px] text-muted-foreground">
            CoreRouter control plane · administrative views are read-only unless a button offers an action
          </footer>
        </div>
      </div>
    </div>
  );
}
