import { AlertTriangle } from "lucide-react";
import { Card } from "@/components/ui/card";
import type { DiffNode, Outcome } from "@/lib/model";
import { packageStyle } from "@/lib/packageColor";
import { packageLabel, packagesOf } from "@/lib/tree";
import { cn } from "@/lib/utils";
import { OpMark } from "./visuals";

interface Props {
  outcomes: Outcome[];
  roots: DiffNode[];
  colors: Map<string, number>;
  selected: string | null;
  onSelect: (id: string) => void;
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
            <Card className={cn("w-full gap-0 rounded-lg py-0 shadow-none transition-colors hover:bg-muted/50", pressed && "ring-2 ring-ring")}>
              <button
                type="button"
                data-filter={outcome.id}
                aria-pressed={pressed}
                onClick={() => onSelect(outcome.id)}
                className="flex w-full flex-col items-start gap-2 rounded-lg p-4 text-left"
              >
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
              </button>
            </Card>
          </li>
        );
      })}
    </ul>
  );
}
