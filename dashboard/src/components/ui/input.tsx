import * as React from 'react';

import { cn } from '@/lib/utils';

const fieldClasses =
  'h-9 w-full rounded-lg border border-white/[0.09] bg-white/[0.025] px-3 text-xs text-foreground transition-colors placeholder:text-neutral-500 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-primary/50 focus-visible:border-primary/60 disabled:cursor-not-allowed disabled:opacity-50';

const Input = React.forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(
  ({ className, type = 'text', ...props }, ref) => (
    <input ref={ref} type={type} className={cn(fieldClasses, className)} {...props} />
  ),
);
Input.displayName = 'Input';

const Select = React.forwardRef<HTMLSelectElement, React.SelectHTMLAttributes<HTMLSelectElement>>(
  ({ className, children, ...props }, ref) => (
    <select ref={ref} className={cn(fieldClasses, 'cursor-pointer pr-8', className)} {...props}>
      {children}
    </select>
  ),
);
Select.displayName = 'Select';

export { Input, Select, fieldClasses };
