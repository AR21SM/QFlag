import { cn } from "@/lib/utils";

export function Progress({ value, className }: { value: number; className?: string }) {
  return <progress max={100} value={Math.max(0, Math.min(100, value))} className={cn("h-1.5 w-full overflow-hidden rounded-full", className)} />;
}
