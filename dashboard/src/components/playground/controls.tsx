'use client';

/**
 * Form controls the Playground needs that the shared kit does not have.
 *
 * These stay local to the Playground rather than growing `components/ui`:
 * a textarea and a switch are only used here, and a switch rendered as a bare
 * checkbox reads poorly in a dense console. They reuse the shared field styling
 * so a Playground input is visually identical to one on any other page.
 */
import { ChevronDown } from 'lucide-react';
import * as React from 'react';

import { Input, fieldClasses } from '@/components/ui/input';
import { cn } from '@/lib/utils';

export function Textarea({
  className,
  ...props
}: React.TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <textarea
      className={cn(
        fieldClasses,
        'h-auto min-h-[72px] resize-y py-2 font-mono text-xs leading-relaxed',
        className,
      )}
      {...props}
    />
  );
}

/** A labelled form row. `hint` explains why the field exists when it is not obvious. */
export function Field({
  label,
  hint,
  htmlFor,
  children,
  className,
  trailing,
}: {
  label: string;
  hint?: React.ReactNode;
  htmlFor?: string;
  children: React.ReactNode;
  className?: string;
  trailing?: React.ReactNode;
}) {
  return (
    <div className={cn('space-y-1.5', className)}>
      <div className="flex items-center justify-between gap-2">
        <label
          htmlFor={htmlFor}
          className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground"
        >
          {label}
        </label>
        {trailing}
      </div>
      {children}
      {hint ? <p className="text-[11px] leading-relaxed text-muted-foreground/80">{hint}</p> : null}
    </div>
  );
}

/** A switch with a visible on/off word, not just a colour. */
export function Toggle({
  checked,
  onChange,
  label,
  hint,
  disabled,
}: {
  checked: boolean;
  onChange: (next: boolean) => void;
  label: string;
  hint?: React.ReactNode;
  disabled?: boolean;
}) {
  return (
    <div className="flex items-start justify-between gap-3">
      <div className="min-w-0 space-y-0.5">
        <p className="text-[13px] font-medium text-foreground">{label}</p>
        {hint ? <p className="text-[11px] leading-relaxed text-muted-foreground">{hint}</p> : null}
      </div>
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-label={label}
        disabled={disabled}
        onClick={() => onChange(!checked)}
        className={cn(
          'mt-0.5 flex h-6 w-11 shrink-0 items-center rounded-full border px-0.5 transition-colors disabled:opacity-50',
          checked
            ? 'border-primary/40 bg-primary/25'
            : 'border-white/[0.1] bg-white/[0.04]',
        )}
      >
        <span
          className={cn(
            'block size-[18px] rounded-full bg-neutral-300 transition-transform',
            checked ? 'translate-x-5 bg-primary' : 'translate-x-0',
          )}
        />
      </button>
    </div>
  );
}

/**
 * A number field that treats an empty box as "not set" rather than as zero.
 *
 * The distinction matters: `temperature: -1` in this app means "omit the field"
 * and `temperature: 0` means a real greedy setting, and sending 0 because the
 * box was empty would change the answer the operator asked for.
 */
export function NumberField({
  label,
  hint,
  value,
  unset,
  step = 1,
  min,
  max,
  onChange,
}: {
  label: string;
  hint?: React.ReactNode;
  value: number;
  /** The value that means "do not send this field". */
  unset: number;
  step?: number;
  min?: number;
  max?: number;
  onChange: (next: number) => void;
}) {
  return (
    <Field label={label} hint={hint}>
      <Input
        type="number"
        step={step}
        min={min}
        max={max}
        value={value === unset ? '' : value}
        placeholder="not sent"
        onChange={(event) => {
          const raw = event.target.value.trim();
          if (raw === '') {
            onChange(unset);
            return;
          }
          const parsed = Number(raw);
          onChange(Number.isFinite(parsed) ? parsed : unset);
        }}
      />
    </Field>
  );
}

/**
 * A collapsible group in the configuration column.
 *
 * The console has more controls than fit on one screen, and collapsing the
 * ones a given test does not need keeps the target and mode visible. Native
 * `details` is used so the behaviour is keyboard-accessible for free.
 */
export function SectionCard({
  title,
  summary,
  defaultOpen = false,
  children,
}: {
  title: string;
  /** A one-line state readout shown even when the group is collapsed. */
  summary?: React.ReactNode;
  defaultOpen?: boolean;
  children: React.ReactNode;
}) {
  return (
    <details
      open={defaultOpen}
      className="group rounded-xl border border-white/[0.07] bg-white/[0.015] open:bg-white/[0.025]"
    >
      <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2.5 text-[13px] font-medium text-foreground [&::-webkit-details-marker]:hidden">
        <ChevronDown className="size-3.5 shrink-0 text-muted-foreground transition-transform group-open:rotate-180" />
        <span className="flex-1">{title}</span>
        <span className="truncate text-[11px] font-normal text-muted-foreground">{summary}</span>
      </summary>
      <div className="space-y-3 border-t border-white/[0.06] px-3 py-3">{children}</div>
    </details>
  );
}
