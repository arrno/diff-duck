import type { ChangeOp, DiffNode } from "./model";

export type Filter = { kind: "all" } | { kind: "unassigned" } | { kind: "outcome"; id: string };

export function matches(node: DiffNode, filter: Filter, unassigned: Set<string>): boolean {
  switch (filter.kind) {
    case "all":
      return true;
    case "unassigned":
      return unassigned.has(node.id);
    case "outcome":
      return (node.outcomes ?? []).includes(filter.id);
  }
}

export function isVisible(node: DiffNode, filter: Filter, unassigned: Set<string>): boolean {
  return matches(node, filter, unassigned) || (node.children ?? []).some((c) => isVisible(c, filter, unassigned));
}

/** IDs of every node that must be open to reveal the filter's matches. */
export function ancestorsOfMatches(roots: DiffNode[], filter: Filter, unassigned: Set<string>): Set<string> {
  const open = new Set<string>();
  const visit = (node: DiffNode): boolean => {
    let any = false;
    for (const child of node.children ?? []) {
      if (visit(child)) any = true;
    }
    if (any) open.add(node.id);
    return any || matches(node, filter, unassigned);
  };
  roots.forEach(visit);
  return open;
}

export function countMatches(roots: DiffNode[], filter: Filter, unassigned: Set<string>): number {
  let count = 0;
  const visit = (node: DiffNode) => {
    const children = node.children ?? [];
    if (children.length === 0 && matches(node, filter, unassigned)) count++;
    children.forEach(visit);
  };
  roots.forEach(visit);
  return count;
}

/** How many descendants of a node the current filter shows. */
export function descendantCount(node: DiffNode, filter: Filter, unassigned: Set<string>): number {
  return (node.children ?? [])
    .filter((c) => isVisible(c, filter, unassigned))
    .reduce((sum, c) => sum + 1 + descendantCount(c, filter, unassigned), 0);
}

/** IDs of every node that can open. */
export function allParents(roots: DiffNode[]): Set<string> {
  const ids = new Set<string>();
  const visit = (n: DiffNode) => {
    if ((n.children ?? []).length > 0) ids.add(n.id);
    (n.children ?? []).forEach(visit);
  };
  roots.forEach(visit);
  return ids;
}

/** How many code units of each operation a subtree holds, ignoring non-code regions. */
export function opCounts(node: DiffNode): Record<ChangeOp, number> {
  const counts: Record<ChangeOp, number> = { ADD: 0, CHANGE: 0, REMOVE: 0 };
  const visit = (n: DiffNode) => {
    const children = n.children ?? [];
    if (children.length === 0 && n.kind !== "REGION") counts[n.op]++;
    children.forEach(visit);
  };
  (node.children ?? []).forEach(visit);
  return counts;
}

/** A package's short name for scanning: its directory's last segment. */
export function packageLabel(node: DiffNode): string {
  if (node.name === "." || node.name === "") return (node.signature ?? "").replace("package ", "") || "root";
  return node.name.split("/").pop() ?? node.name;
}

export function packagesOf(roots: DiffNode[], outcomeId: string): DiffNode[] {
  const within = (n: DiffNode): boolean =>
    (n.outcomes ?? []).includes(outcomeId) || (n.children ?? []).some(within);
  return roots.filter(within);
}

/** Largest change first, so the most significant package leads. */
export function bySize(a: DiffNode, b: DiffNode): number {
  return b.added + b.removed - (a.added + a.removed) || a.name.localeCompare(b.name);
}
