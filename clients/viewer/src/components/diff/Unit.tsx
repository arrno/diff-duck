import type { ReactNode } from "react";
import { AlertTriangle, Package } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { DiffNode } from "@/lib/model";
import { packageStyle } from "@/lib/packageColor";
import { abbreviate, descendantCount, isVisible, matches, packageLabel } from "@/lib/tree";
import { cn } from "@/lib/utils";
import { SourceDiff } from "./SourceDiff";
import { useView } from "./ViewState";
import { Chevron, KindIcon, LineStats, OpBadge } from "./visuals";

/**
 * A name that may be long, such as a file path: shortened in the middle, wrapping within its own
 * column so the icon and badge before it stay put. A shortened name shows the full one in a
 * tooltip on hover, and on focus when `focusable`; a screen reader always hears the full name.
 * Inside a button, leave `focusable` off: a control may not hold another one.
 */
function Name({ name, className, focusable = false }: { name: string; className?: string; focusable?: boolean }) {
  const short = abbreviate(name);
  const style = cn("min-w-0 py-1 font-mono [overflow-wrap:anywhere]", className);
  if (short === name) return <span className={style}>{name}</span>;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          data-testid="abbreviated-name"
          tabIndex={focusable ? 0 : undefined}
          className={cn(style, "cursor-default underline decoration-dotted underline-offset-4 focus-visible:rounded-sm focus-visible:outline-2 focus-visible:outline-ring")}
        >
          <span aria-hidden>{short}</span>
          <span className="sr-only">{name}</span>
        </span>
      </TooltipTrigger>
      <TooltipContent>{name}</TooltipContent>
    </Tooltip>
  );
}

/**
 * The fixed columns that start a row: the toggle or its space, the kind, and the change. They
 * sit on the first line however far the rest of the row wraps.
 */
function Lead({ children }: { children: ReactNode }) {
  return <span className="flex h-7 shrink-0 items-center gap-3">{children}</span>;
}

/** The rest of a row, which wraps within its own column. */
const rest = "flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-1";

function SummaryText({ node, className }: { node: DiffNode; className?: string }) {
  const { text, origin } = node.summary;
  if (text) return <p data-testid="summary" className={cn("max-w-prose", className)}>{text}</p>;
  if (origin === "none") return <p data-testid="no-summary" className={cn("text-warn", className)}>No explanation available.</p>;
  return null;
}

function Uncertainty({ node }: { node: DiffNode }) {
  const { uncertainty, confidence } = node.summary;
  if (!uncertainty && confidence !== "low") return null;
  return (
    <p data-testid="uncertainty" className="flex max-w-prose items-start gap-2 text-xs text-warn">
      <AlertTriangle aria-hidden className="mt-0.5 size-3 shrink-0" />
      <span>Uncertain: {uncertainty || "low confidence"}</span>
    </p>
  );
}

/** Facts about identity that a reader needs without opening anything. */
function IdentityTags({ node }: { node: DiffNode }) {
  const tag = "rounded border px-1.5 py-0.5 text-xs text-muted-foreground";
  return (
    <>
      {node.test && <span className={tag}>test</span>}
      {node.moved && <span className={tag}>moved</span>}
      {!node.direct && node.kind !== "PACKAGE" && <span className={tag}>inner changes only</span>}
    </>
  );
}

function PreviousIdentity({ node }: { node: DiffNode }) {
  const { semanticName } = node.summary;
  return (
    <>
      {semanticName && node.kind !== "PACKAGE" && <p className="font-mono text-xs break-words text-muted-foreground">AKA {node.name}</p>}
      {node.previousName && <p className="font-mono text-xs break-words text-muted-foreground">Was {node.previousName}</p>}
    </>
  );
}

function SourceToggle({ node }: { node: DiffNode }) {
  const { source, toggleSource } = useView();
  const showing = source.has(node.id);
  return (
    <Button
      type="button"
      variant="link"
      size="sm"
      className="h-auto p-0 text-xs text-muted-foreground"
      aria-expanded={showing}
      aria-label={`${showing ? "Hide" : "Show"} source of ${node.name}`}
      onClick={() => toggleSource(node.id)}
    >
      {showing ? "Hide source" : "Show source"}
    </Button>
  );
}

function Details({ node }: { node: DiffNode }) {
  const { source } = useView();
  if (!source.has(node.id)) return null;
  return (
    <div className="space-y-1 pt-1">
      {node.signature && node.signature !== node.name && <p className="font-mono text-xs break-words">{node.signature}</p>}
      {node.previousSignature && <p className="font-mono text-xs break-words text-muted-foreground">Was: {node.previousSignature}</p>}
      <SourceDiff node={node} />
    </div>
  );
}

/** A one-line fact, such as a field or a constant, where the signature is the content. */
function CompactRow({ node }: { node: DiffNode }) {
  return (
    <li data-node-id={node.id} className="rounded-md py-1 hover:bg-muted/50">
      <div className="flex items-start gap-3">
        <Lead>
          <span aria-hidden className="size-7 shrink-0" />
          <KindIcon kind={node.kind} />
          <OpBadge op={node.op} />
        </Lead>
        <span className={rest}>
          <Name name={node.signature || node.name} className="text-sm" focusable />
          <IdentityTags node={node} />
          <SourceToggle node={node} />
        </span>
      </div>
      <div className="ml-17 space-y-1 empty:hidden">
        {node.previousSignature && <p className="font-mono text-xs break-words text-muted-foreground">Was: {node.previousSignature}</p>}
        {node.summary.text && <p className="max-w-prose text-sm text-muted-foreground">{node.summary.text}</p>}
        <Uncertainty node={node} />
        <Details node={{ ...node, signature: undefined, previousSignature: undefined }} />
      </div>
    </li>
  );
}

/** A behavior-bearing unit: name first, then what changed about it. */
function LeafRow({ node }: { node: DiffNode }) {
  return (
    <li data-node-id={node.id} className="rounded-md py-2 hover:bg-muted/50">
      <div className="flex items-start gap-3">
        <Lead>
          <span aria-hidden className="size-7 shrink-0" />
          <KindIcon kind={node.kind} />
          <OpBadge op={node.op} />
        </Lead>
        <span className={rest}>
          <Name name={node.summary.semanticName || node.name} className="text-sm font-semibold" focusable />
          <IdentityTags node={node} />
          <span className="ml-auto"><LineStats added={node.added} removed={node.removed} /></span>
        </span>
      </div>
      <div className="mt-1 ml-17 space-y-1">
        <PreviousIdentity node={node} />
        <SummaryText node={node} className="text-sm" />
        <Uncertainty node={node} />
        <SourceToggle node={node} />
        <Details node={node} />
      </div>
    </li>
  );
}

/** Any unit that contains others, from a package down: one row of a file tree, with no box. */
function GroupRow({ node, colors }: { node: DiffNode; colors: Map<string, number> }) {
  const { expanded, toggle, filter, unassigned } = useView();
  const open = expanded.has(node.id);
  const children = (node.children ?? []).filter((c) => isVisible(c, filter, unassigned));
  const isPackage = node.kind === "PACKAGE";
  const label = isPackage ? packageLabel(node) : "";
  return (
    <li data-node-id={node.id} className="py-1">
      <Collapsible open={open} onOpenChange={() => toggle(node.id)}>
        <CollapsibleTrigger
          data-toggle={node.id}
          aria-label={`${open ? "Collapse" : "Expand"} ${node.name}`}
          style={isPackage ? packageStyle(colors.get(label)) : undefined}
          className="group flex w-full items-start gap-3 rounded-md py-1 text-left hover:bg-muted/50"
        >
          <Lead>
            <Chevron open={open} />
            {isPackage ? <Package aria-hidden className="size-4 shrink-0 [color:var(--pkg)]" /> : <KindIcon kind={node.kind} />}
            <OpBadge op={node.op} />
          </Lead>
          <span className={rest}>
            {isPackage ? (
              <span data-testid="package-name" className="min-w-0 text-lg font-semibold [overflow-wrap:anywhere] [color:var(--pkg)]">{label}</span>
            ) : (
              <Name name={node.summary.semanticName || node.name} className="py-0.5 text-base font-semibold" />
            )}
            {isPackage && node.name !== label && <Name name={node.name} className="text-xs text-muted-foreground" />}
            <IdentityTags node={node} />
            <span data-testid="descendants" className="text-xs text-muted-foreground">{descendantCount(node, filter, unassigned)} inside</span>
            <span className="ml-auto"><LineStats added={node.added} removed={node.removed} /></span>
          </span>
        </CollapsibleTrigger>
        <div className="ml-17 space-y-1">
          <PreviousIdentity node={node} />
          <SummaryText node={node} className={isPackage ? "text-base" : "text-sm"} />
          <Uncertainty node={node} />
          <SourceToggle node={node} />
          <Details node={node} />
        </div>
        <CollapsibleContent>
          <UnitList nodes={children} colors={colors} className="mt-1 ml-3.5 border-l-2 pl-3" />
        </CollapsibleContent>
      </Collapsible>
    </li>
  );
}

function RegionRow({ node }: { node: DiffNode }) {
  return (
    <li data-node-id={node.id} className="rounded-md py-1 text-muted-foreground hover:bg-muted/50">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
        <span aria-hidden className="size-7 shrink-0" />
        <KindIcon kind={node.kind} />
        <Name name={node.name} focusable />
        <span className="max-w-prose min-w-0">{node.summary.text}</span>
        <LineStats added={node.added} removed={node.removed} />
        <SourceToggle node={node} />
      </div>
      <div className="ml-17"><Details node={node} /></div>
    </li>
  );
}

function isCompact(node: DiffNode): boolean {
  return node.kind === "FIELD" || node.kind === "CONST" || node.kind === "VAR" || node.kind === "INTERFACE_METHOD" ||
    node.kind === "ENUM_MEMBER" ||
    ((node.kind === "STRUCT" || node.kind === "INTERFACE" || node.kind === "TYPE") && node.summary.origin === "structural" && !node.summary.text);
}

export function UnitList({ nodes, colors, className }: { nodes: DiffNode[]; colors: Map<string, number>; className?: string }) {
  const { filter, unassigned } = useView();
  return (
    <ul className={className}>
      {nodes.map((node) => {
        // Ancestors stay for context, and non-matching leaves leave the view.
        if (filter.kind !== "all" && !matches(node, filter, unassigned) && !(node.children ?? []).length) return null;
        if (node.kind === "REGION") return <RegionRow key={node.id} node={node} />;
        if ((node.children ?? []).length > 0) return <GroupRow key={node.id} node={node} colors={colors} />;
        return isCompact(node) ? <CompactRow key={node.id} node={node} /> : <LeafRow key={node.id} node={node} />;
      })}
    </ul>
  );
}
