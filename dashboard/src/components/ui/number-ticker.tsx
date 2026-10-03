'use client';

import { useInView, useMotionValue, useReducedMotion, useSpring } from 'motion/react';
import * as React from 'react';

import { cn } from '@/lib/utils';

export interface NumberTickerProps extends React.ComponentPropsWithoutRef<'span'> {
  /** The value to settle on. */
  value: number;
  /** The value counting starts from. */
  startValue?: number;
  /** Count up to `value`, or down from it. */
  direction?: 'up' | 'down';
  /** Seconds to wait before counting, for staggering a row of cards. */
  delay?: number;
  /** Decimal places to render. */
  decimalPlaces?: number;
}

/**
 * Counts a number up (or down) to a target, once it scrolls into view.
 *
 * Formatting is left to the caller. This renders a bare number so it can be
 * dropped inside an existing figure — a currency, a percentage, a card value —
 * without that figure losing its prefix, symbol or suffix. Use `decimalPlaces`
 * for the rounding and pair it with the same formatter the surrounding text
 * uses, so an animating figure and a settled one are indistinguishable.
 *
 * `prefers-reduced-motion` is honoured by rendering the final value outright:
 * a spring that travels is exactly the kind of movement the setting exists to
 * suppress, and these figures sit on the page an operator stares at all day.
 */
export function NumberTicker({
  value,
  startValue = 0,
  direction = 'up',
  delay = 0,
  className,
  decimalPlaces = 0,
  ...props
}: NumberTickerProps) {
  const ref = React.useRef<HTMLSpanElement>(null);
  const reduceMotion = useReducedMotion();

  const target = direction === 'down' ? startValue : value;
  const origin = direction === 'down' ? value : startValue;

  const motionValue = useMotionValue(reduceMotion ? target : origin);
  const springValue = useSpring(motionValue, { damping: 60, stiffness: 100 });
  const isInView = useInView(ref, { once: true, margin: '0px' });

  React.useEffect(() => {
    if (!isInView) return;
    // Reduced motion has already been applied to the motion value's origin, so
    // there is nothing to schedule.
    if (reduceMotion) return;
    const timer = setTimeout(() => {
      motionValue.set(target);
    }, delay * 1000);
    return () => clearTimeout(timer);
  }, [motionValue, isInView, delay, reduceMotion, target]);

  // Written straight to the node rather than held in state: the spring emits on
  // every frame, and a re-render per frame for a value that is already on
  // screen is the one thing that makes a counter feel expensive.
  React.useEffect(
    () =>
      springValue.on('change', (latest) => {
        if (!ref.current) return;
        ref.current.textContent = Intl.NumberFormat('en-US', {
          minimumFractionDigits: decimalPlaces,
          maximumFractionDigits: decimalPlaces,
        }).format(Number(latest.toFixed(decimalPlaces)));
      }),
    [springValue, decimalPlaces],
  );

  const initial = reduceMotion ? target : origin;

  return (
    <span
      ref={ref}
      className={cn('inline-block tabular-nums', className)}
      {...props}
    >
      {Intl.NumberFormat('en-US', {
        minimumFractionDigits: decimalPlaces,
        maximumFractionDigits: decimalPlaces,
      }).format(initial)}
    </span>
  );
}