import { useCallback, useEffect, useMemo, useState } from "react";
import { AlertTriangle, Moon, Sun } from "lucide-react";
import { useTheme } from "next-themes";
import { Button } from "@/components/ui/button";
import { SiteFooter } from "@/components/SiteFooter";
import { Outcomes, ScopeFilters } from "@/components/diff/Outcomes";
import { UnitList } from "@/components/diff/Unit";
import { ViewContext, type ViewState } from "@/components/diff/ViewState";
import type { CompressedDiff } from "@/lib/model";
import { assignPackageColors } from "@/lib/packageColor";
import { allParents, ancestorsOfMatches, bySize, countMatches, isVisible, packageLabel, type Filter } from "@/lib/tree";

function toggled(set: Set<string>, id: string): Set<string> {
  const next = new Set(set);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  return next;
}

function ThemeToggle() {
  const { resolvedTheme, setTheme } = useTheme();
  const dark = resolvedTheme === "dark";
  return (
    <Button type="button" variant="outline" size="icon" aria-label="Toggle theme" onClick={() => setTheme(dark ? "light" : "dark")}>
      {dark ? <Sun aria-hidden /> : <Moon aria-hidden />}
    </Button>
  );
}

function DuckIcon() {
  return (
    <svg aria-hidden viewBox="0 0 32 32" className="size-7">
      <path fill="#FBBF24" d="M3 13c1.5 3 4.5 4.2 8.5 4L20 17c3.5 0 6.5 2.2 6.5 5.2 0 3.6-4 6-9.5 6-7.5 0-13.5-4.2-14-15.2z" />
      <circle cx="20" cy="11" r="6.5" fill="#FBBF24" />
      <path fill="#F97316" d="M26 8.8 31.5 11 26 13.8z" />
      <circle cx="21.8" cy="9.5" r="1.2" fill="#1C1917" />
      <path fill="none" stroke="#D97706" strokeLinecap="round" strokeWidth="1.8" d="M9.5 20.5c3 3 7.5 3 10.5-.3" />
    </svg>
  );
}

export function App({ model }: { model: CompressedDiff }) {
  const packages = useMemo(() => model.nodes.filter((n) => n.kind === "PACKAGE").sort(bySize), [model]);
  const otherFiles = useMemo(() => model.nodes.filter((n) => n.kind !== "PACKAGE"), [model]);
  useEffect(() => {
    const repository = model.repository.replace(/\/+$/, "").split("/").pop()?.replace(/\.git$/, "");
    document.title = `${repository}: ${model.base} → ${model.head} · DuckDiff`;
  }, [model]);
  const [filter, setFilter] = useState<Filter>({ kind: "all" });
  // Packages start open: opening a card only to reach more cards is a wasted click.
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set(packages.map((n) => n.id)));
  const [savedExpanded, setSavedExpanded] = useState<Set<string>>(expanded);
  const [source, setSource] = useState<Set<string>>(new Set());

  const unassigned = useMemo(() => new Set(model.unassigned ?? []), [model]);
  const colors = useMemo(() => assignPackageColors(packages.map(packageLabel)), [packages]);

  const applyFilter = useCallback((next: Filter) => {
    if (filter.kind === "all") setSavedExpanded(expanded);
    setFilter(next);
    setExpanded(next.kind === "all" ? (filter.kind === "all" ? expanded : savedExpanded) : ancestorsOfMatches(model.nodes, next, unassigned));
  }, [filter, expanded, savedExpanded, model, unassigned]);

  const selectOutcome = useCallback((id: string) => {
    applyFilter(filter.kind === "outcome" && filter.id === id ? { kind: "all" } : { kind: "outcome", id });
  }, [filter, applyFilter]);

  const view: ViewState = {
    filter,
    unassigned,
    expanded,
    toggle: (id) => setExpanded((s) => toggled(s, id)),
    source,
    toggleSource: (id) => setSource((s) => toggled(s, id)),
  };

  const visiblePackages = packages.filter((n) => isVisible(n, filter, unassigned));
  const visibleFiles = otherFiles.filter((n) => isVisible(n, filter, unassigned));
  const status =
    filter.kind === "all" ? "Showing all changes."
    : filter.kind === "unassigned" ? `Showing ${unassigned.size} changes with no outcome.`
    : `Showing ${countMatches(model.nodes, filter, unassigned)} changes for: ${model.outcomes.find((o) => o.id === filter.id)?.title}.`;

  return (
    <ViewContext.Provider value={view}>
      <a href="#changes" className="sr-only focus:not-sr-only focus:absolute focus:z-[60] focus:m-2 focus:rounded-md focus:bg-primary focus:px-4 focus:py-2 focus:text-primary-foreground">
        Skip to changed code
      </a>
      <header className="sticky top-0 z-50 border-b bg-background/85 backdrop-blur">
        <div className="mx-auto flex h-14 max-w-screen-2xl items-center justify-between gap-4 px-4 md:px-8">
          <span className="flex items-center gap-2 text-base font-semibold tracking-tight">
            <DuckIcon />
            DuckDiff
          </span>
          <ThemeToggle />
        </div>
      </header>
      <main className="mx-auto max-w-screen-2xl space-y-8 px-4 py-6 md:px-8 md:py-8">
        <div className="space-y-4">
          <div className="min-w-0 space-y-1">
            <h1 className="text-sm font-medium text-muted-foreground">Compressed diff</h1>
            <p className="font-mono text-xs break-words text-muted-foreground">
              {model.repository} · {model.base} ({model.baseSha.slice(0, 8)}) → {model.head} ({model.headSha.slice(0, 8)})
            </p>
          </div>
          {model.overview && <p data-testid="overview" className="max-w-4xl text-2xl leading-snug font-semibold">{model.overview}</p>}
          <p data-testid="stats" className="flex flex-wrap gap-x-4 gap-y-1 text-sm text-muted-foreground">
            <span>{model.files} files</span>
            <span className="font-mono"><span className="text-add">+{model.added}</span> <span className="text-remove">−{model.removed}</span></span>
            <span>traced {model.coverage.tracedLines}/{model.coverage.changedLines} changed lines</span>
          </p>
        </div>

        {(model.warnings ?? []).length > 0 && (
          <section data-testid="warnings" className="rounded-lg border border-warn bg-warn-soft p-4 text-sm">
            <p className="flex items-center gap-2 font-medium text-warn">
              <AlertTriangle aria-hidden className="size-4" />
              This output has {model.warnings?.length} warning(s)
            </p>
            <ul className="mt-2 space-y-1 text-xs">
              {model.warnings?.map((w, i) => <li key={i} className="break-words">{w}</li>)}
            </ul>
          </section>
        )}

        <div className="grid grid-cols-1 gap-8 lg:grid-cols-[24rem_minmax(0,1fr)]">
          {/* Stays in view while the tree scrolls, so an outcome is always one click away. */}
          <section
            aria-labelledby="outcomes-heading"
            tabIndex={0}
            className="space-y-3 lg:sticky lg:top-18 lg:max-h-[calc(100vh-5.5rem)] lg:self-start lg:overflow-y-auto lg:p-1"
          >
            <h2 id="outcomes-heading" className="text-lg font-medium">Outcomes</h2>
            <ScopeFilters
              scope={filter.kind === "outcome" ? null : filter.kind}
              unassignedCount={unassigned.size}
              onSelect={(kind) => applyFilter({ kind })}
            />
            <Outcomes
              outcomes={model.outcomes}
              roots={model.nodes}
              colors={colors}
              selected={filter.kind === "outcome" ? filter.id : null}
              onSelect={selectOutcome}
            />
          </section>

          <section id="changes" aria-labelledby="changes-heading" tabIndex={-1} className="min-w-0 scroll-mt-18 space-y-4 lg:pt-1">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <h2 id="changes-heading" className="text-lg font-medium">What changed</h2>
              <div className="flex flex-wrap gap-2">
                <Button type="button" size="sm" onClick={() => setExpanded(allParents(model.nodes))}>Expand all</Button>
                <Button type="button" size="sm" onClick={() => setExpanded(new Set())}>Collapse all</Button>
              </div>
            </div>
            <p role="status" data-testid="filter-status" className="text-xs text-muted-foreground">{status}</p>

            <div data-testid="tree" className="space-y-4">
              {visiblePackages.length > 0 && <UnitList nodes={visiblePackages} colors={colors} className="space-y-3" />}
              {visibleFiles.length > 0 && (
                <div>
                  <h3 className="mb-1 text-sm font-medium text-muted-foreground">Other files</h3>
                  <UnitList nodes={visibleFiles} colors={colors} className="mt-1" />
                </div>
              )}
              {visiblePackages.length === 0 && visibleFiles.length === 0 && (
                <p className="text-muted-foreground">{model.nodes.length ? "Nothing matches this filter." : "No changes between these commits."}</p>
              )}
            </div>
          </section>
        </div>

        <footer data-testid="generation" className="border-t pt-4 text-xs text-muted-foreground">
          Model: {model.generation.model}. Prompts: {model.generation.promptVariant}. {model.metrics.compressionRatio.toFixed(1)}x compression in the opening view.
          {" "}A summary explains apparent behavior; it does not verify the code is correct.
        </footer>
      </main>
      <SiteFooter />
    </ViewContext.Provider>
  );
}
