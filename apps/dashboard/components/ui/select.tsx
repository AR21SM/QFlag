import * as React from "react";
import { ChevronDown } from "lucide-react";
import { cn } from "@/lib/utils";

export const Select = React.forwardRef<HTMLSelectElement, React.SelectHTMLAttributes<HTMLSelectElement>>(
  ({ className, children, ...props }, ref) => (
    <span className="relative inline-flex">
      <select
        ref={ref}
        className={cn(
          "h-9 appearance-none rounded-md border border-input bg-background px-3 pr-8 text-sm text-foreground outline-none transition-colors focus-visible:ring-1 focus-visible:ring-ring disabled:opacity-50",
          className,
        )}
        {...props}
      >
        {children}
      </select>
      <ChevronDown className="pointer-events-none absolute right-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
    </span>
  ),
);
Select.displayName = "Select";
