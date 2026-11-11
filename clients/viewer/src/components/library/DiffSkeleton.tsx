import type { ReactNode } from "react";
import { Navbar } from "@/components/Navbar";
import { SiteFooter } from "@/components/SiteFooter";
import { Skeleton } from "@/components/ui/skeleton";

/** The frame of a diff page, as wide as a finished diff, so nothing jumps when it arrives. */
export function DiffShell({ home, children }: { home: string; children: ReactNode }) {
  return (
    <div className="flex min-h-screen flex-col">
      <Navbar home={home} />
      <main className="mx-auto w-full max-w-screen-2xl flex-1 space-y-8 px-4 py-6 md:px-8 md:py-8">{children}</main>
      <SiteFooter />
    </div>
  );
}

// One bar for each line of changes.
const LINES = 12;

/**
 * The shape of a diff: its overview, then outcome cards in a narrow column beside full-width lines
 * where the changes will be, laid out as a finished diff is.
 */
export function DiffBodySkeleton() {
  return (
    <div className="space-y-8">
      <div className="max-w-4xl space-y-3">
        <Skeleton className="h-6 w-full" />
        <Skeleton className="h-6 w-2/3" />
      </div>
      <div className="grid grid-cols-1 gap-8 lg:grid-cols-[24rem_minmax(0,1fr)]">
        <div className="space-y-3">
          {[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-24 rounded-lg" />)}
        </div>
        <div className="space-y-3 pt-1">
          {Array.from({ length: LINES }, (_, i) => <Skeleton key={i} className="h-6 w-full" />)}
        </div>
      </div>
    </div>
  );
}

/** A whole diff page, while it loads. */
export function DiffPageSkeleton({ home }: { home: string }) {
  return (
    <DiffShell home={home}>
      <div role="status" data-testid="diff-loading" className="space-y-8">
        <h1 className="sr-only">Loading this diff…</h1>
        <Skeleton className="h-4 w-48" />
        <DiffBodySkeleton />
      </div>
    </DiffShell>
  );
}
