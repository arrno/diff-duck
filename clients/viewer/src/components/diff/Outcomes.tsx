import type { ReactNode } from "react";
import { AlertTriangle, CircleDashed, Layers } from "lucide-react";
import { Card } from "@/components/ui/card";
import type { DiffNode, Outcome } from "@/lib/model";
import { packageStyle } from "@/lib/packageColor";
import { packageLabel, packagesOf } from "@/lib/tree";
import { cn } from "@/lib/utils";
import { IconMark, OpMark } from "./visuals";

interface Props {
  outcomes: Outcome[];
  roots: DiffNode[];
  colors: Map<string, number>;
  selected: string | null;
  onSelect: (id: string) => void;
}

/** A card that selects a filter. Every filter in the column shares this look. */
function FilterCard({ filter, pressed, onClick, className, children }: {
  filter: string;
  pressed: boolean;
  onClick: () => void;
  className?: string;
  children: ReactNode;
}) {
  return (
    <Card className={cn("h-full w-full gap-0 rounded-lg py-0 shadow-none transition-colors hover:bg-muted/50", pressed && "ring-2 ring-ring")}>
      <button type="button" data-filter={filter} aria-pressed={pressed} onClick={onClick} className={cn("flex h-full w-full rounded-lg text-left", className)}>
        {children}
      </button>
    </Card>
  );
}

export function ScopeFilters({ scope, unassignedCount, onSelect }: {
  scope: "all" | "unassigned" | null;
  unassignedCount: number;
  onSelect: (scope: "all" | "unassigned") => void;
}) {
  const layout = "items-center gap-2 p-3 text-sm leading-snug font-semibold";
  return (
    <div className="grid grid-cols-2 gap-2">
      <FilterCard filter="all" pressed={scope === "all"} onClick={() => onSelect("all")} className={layout}>
        <span className="text-change"><IconMark icon={Layers} soft="bg-change-soft" /></span>
        All changes
      </FilterCard>
      <FilterCard filter="unassigned" pressed={scope === "unassigned"} onClick={() => onSelect("unassigned")} className={layout}>
        <span className="text-warn"><IconMark icon={CircleDashed} soft="bg-warn-soft" /></span>
        No outcome ({unassignedCount})
      </FilterCard>
    </div>
  );
}

function PackageChip({ node, colors }: { node: DiffNode; colors: Map<string, number> }) {
  const label = packageLabel(node);
  return (
    <span style={packageStyle(colors.get(label))} className="inline-flex items-center gap-1.5 text-xs font-medium [color:var(--pkg)]">
      <span aria-hidden className="size-2 rounded-full [background-color:var(--pkg)]" />
      {label}
    </span>
  );
}

export function Outcomes({ outcomes, roots, colors, selected, onSelect }: Props) {
  if (outcomes.length === 0) {
    return <p data-testid="outcomes" className="text-sm text-muted-foreground">No outcomes were identified.</p>;
  }
  return (
    <ul data-testid="outcomes" className="space-y-3">
      {outcomes.map((outcome) => {
        const pressed = selected === outcome.id;
        const packages = packagesOf(roots, outcome.id).filter((n) => n.kind === "PACKAGE");
        return (
          <li key={outcome.id}>
            <FilterCard filter={outcome.id} pressed={pressed} onClick={() => onSelect(outcome.id)} className="flex-col items-start gap-2 p-4">
              <OpMark op={outcome.op} />
              <span className="text-base leading-snug font-semibold">{outcome.title}</span>
              {outcome.description && <span className="text-sm text-muted-foreground">{outcome.description}</span>}
              {outcome.confidence === "low" && (
                <span className="flex items-start gap-2 text-xs text-warn">
                  <AlertTriangle aria-hidden className="mt-0.5 size-3 shrink-0" />
                  Low confidence{outcome.uncertainty ? `: ${outcome.uncertainty}` : ""}
                </span>
              )}
              {packages.length > 0 && (
                <span className="flex flex-wrap gap-x-3 gap-y-1 pt-1">
                  {packages.map((p) => <PackageChip key={p.id} node={p} colors={colors} />)}
                </span>
              )}
            </FilterCard>
          </li>
        );
      })}
    </ul>
  );
}
