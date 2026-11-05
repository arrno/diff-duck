// The compressed diff as duck-diff publishes it. The viewer only presents this model.

export type ChangeOp = "ADD" | "REMOVE" | "CHANGE";
export type UnitKind =
  | "PACKAGE" | "STRUCT" | "INTERFACE" | "TYPE" | "FUNCTION" | "METHOD"
  | "INTERFACE_METHOD" | "FIELD" | "CONST" | "VAR" | "FILE" | "REGION";

export interface DiffLine {
  kind: "context" | "added" | "removed" | "gap";
  oldLine?: number;
  newLine?: number;
  text?: string;
}

export interface FileDiff {
  oldPath?: string;
  newPath?: string;
  binary?: boolean;
  lines?: DiffLine[];
}

export interface Summary {
  semanticName?: string;
  text?: string;
  confidence?: "high" | "low";
  uncertainty?: string;
  origin: "none" | "structural" | "model";
}

export interface SourceLocation {
  path: string;
  side: "base" | "head";
  startLine?: number;
  endLine?: number;
}

export interface DiffNode {
  id: string;
  kind: UnitKind;
  name: string;
  previousName?: string;
  signature?: string;
  previousSignature?: string;
  op: ChangeOp;
  direct: boolean;
  moved?: boolean;
  test?: boolean;
  locations?: SourceLocation[];
  added: number;
  removed: number;
  summary: Summary;
  outcomes?: string[];
  diff?: FileDiff[];
  children?: DiffNode[];
}

export interface Outcome {
  id: string;
  op: ChangeOp;
  title: string;
  description?: string;
  nodeIds: string[];
  confidence?: "high" | "low";
  uncertainty?: string;
}

export interface CompressedDiff {
  repository: string;
  base: string;
  head: string;
  baseSha: string;
  headSha: string;
  overview?: string;
  outcomes: Outcome[];
  nodes: DiffNode[];
  unassigned: string[];
  files: number;
  added: number;
  removed: number;
  coverage: { changedLines: number; tracedLines: number; changedFiles: number; tracedFiles: number };
  generation: { model: string; promptVariant: string };
  metrics: { compressionRatio: number };
  warnings?: string[];
}

export function loadModel(): CompressedDiff {
  const element = document.getElementById("model");
  return JSON.parse(element?.textContent ?? "") as CompressedDiff;
}
