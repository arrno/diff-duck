import { cn } from "@/lib/utils"

/** A placeholder block for content that is still loading. It stops pulsing when motion is reduced. */
function Skeleton({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="skeleton"
      aria-hidden
      className={cn("animate-pulse rounded-md bg-accent motion-reduce:animate-none", className)}
      {...props}
    />
  )
}

export { Skeleton }
