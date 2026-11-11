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

/**
 * The packages that hold an outcome's changes directly, at any depth: a folder appears when one
 * of its own units belongs to the outcome, not merely because a subfolder's does.
 */
export function packagesOf(roots: DiffNode[], outcomeId: string): DiffNode[] {
  const found: DiffNode[] = [];
  const holds = (n: DiffNode): boolean =>
    (n.outcomes ?? []).includes(outcomeId) || (n.children ?? []).some((c) => c.kind !== "PACKAGE" && holds(c));
  const visit = (n: DiffNode) => {
    if (n.kind === "PACKAGE" && holds(n)) found.push(n);
    (n.children ?? []).forEach(visit);
  };
  roots.forEach(visit);
  return found;
}

/** Every package in the tree, nested ones included, parents first. */
export function allPackages(roots: DiffNode[]): DiffNode[] {
  const found: DiffNode[] = [];
  const visit = (n: DiffNode) => {
    if (n.kind === "PACKAGE") found.push(n);
    (n.children ?? []).forEach(visit);
  };
  roots.forEach(visit);
  return found;
}

/**
 * What a package row is called: inside another package, its path below that one, such as
 * "components/session-clock"; otherwise its own label.
 */
export function packageTitle(node: DiffNode, parent?: string): string {
  return parent && node.name.startsWith(`${parent}/`) ? node.name.slice(parent.length + 1) : packageLabel(node);
}

/** Largest change first, so the most significant package leads. */
/**
 * Orders packages by directory, one path segment at a time, so a directory's subdirectories
 * follow it directly: "src", "src/components/x", then "src-utils". Packages that share a
 * directory, such as two C# namespaces, follow their label.
 */
export function byPath(a: DiffNode, b: DiffNode): number {
  const x = a.name.split("/"), y = b.name.split("/");
  for (let i = 0; i < Math.min(x.length, y.length); i++) {
    if (x[i] !== y[i]) return x[i] < y[i] ? -1 : 1;
  }
  return x.length - y.length || (a.signature ?? "").localeCompare(b.signature ?? "");
}

/**
 * Shortens a long name in the middle with "…". A path keeps its first directory and as many of
 * its last segments as fit, since the file name is what tells paths apart.
 */
export function abbreviate(name: string, max = 72): string {
  if (name.length <= max) return name;
  const parts = name.split("/");
  if (parts.length > 2) {
    let tail = parts[parts.length - 1];
    for (let i = parts.length - 2; i > 0 && parts[0].length + tail.length + parts[i].length + 4 <= max; i--) {
      tail = `${parts[i]}/${tail}`;
    }
    const short = `${parts[0]}/…/${tail}`;
    if (short.length <= max) return short;
  }
  const front = Math.ceil((max - 1) * 0.4);
  return `${name.slice(0, front)}…${name.slice(name.length - (max - 1 - front))}`;
}
