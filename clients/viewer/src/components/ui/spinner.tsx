import { LoaderCircle } from "lucide-react"

import { cn } from "@/lib/utils"

/**
 * An animated loading indicator. With a `label` it announces itself as a status; without one it
 * is decoration beside text that already says what is happening. It stops spinning when motion
 * is reduced.
 */
function Spinner({ className, label, ...props }: React.ComponentProps<"svg"> & { label?: string }) {
  return (
    <LoaderCircle
      data-slot="spinner"
      role={label ? "status" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      className={cn("size-4 shrink-0 animate-spin motion-reduce:animate-none", className)}
      {...props}
    />
  )
}

export { Spinner }
