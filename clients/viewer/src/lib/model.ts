// The compressed diff as diffduck publishes it. The viewer only presents this model.

export type ChangeOp = "ADD" | "REMOVE" | "CHANGE";
export type UnitKind =
  | "PACKAGE" | "STRUCT" | "INTERFACE" | "TYPE" | "FUNCTION" | "METHOD"
  | "INTERFACE_METHOD" | "FIELD" | "CONST" | "VAR" | "FILE" | "REGION"
  | "MODULE" | "CLASS" | "ENUM" | "ENUM_MEMBER" | "TRAIT" | "RECORD" | "PROPERTY";

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
  generation: Generation;
  metrics: { compressionRatio: number };
  warnings?: string[];
}

export type ProviderId = "claude-code" | "codex" | "anthropic-api" | "openai-api";
export type ReadingLevelId = "2nd-grade" | "6th-grade" | "high-school" | "college" | "phd";

/** How a diff's summaries were written. Diffs saved before provider and reading level were recorded lack them. */
export interface Generation {
  model: string;
  promptVariant: string;
  provider?: ProviderId;
  readingLevel?: ReadingLevelId;
}

const PROVIDER_LABELS: Record<ProviderId, string> = {
  "claude-code": "Claude Code",
  codex: "Codex",
  "anthropic-api": "Anthropic API",
  "openai-api": "OpenAI API",
};

/** The five reading levels, plainest first. */
export const READING_LEVELS: { id: ReadingLevelId; label: string }[] = [
  { id: "2nd-grade", label: "2nd grade" },
  { id: "6th-grade", label: "6th grade" },
  { id: "high-school", label: "High school" },
  { id: "college", label: "College" },
  { id: "phd", label: "PhD" },
];

/** A provider's name as a person reads it, or the ID itself if this viewer does not know it. */
export const providerLabel = (id: string) => PROVIDER_LABELS[id as ProviderId] ?? id;
/** A reading level's name as a person reads it, or the ID itself if this viewer does not know it. */
export const readingLevelLabel = (id: string) => READING_LEVELS.find((l) => l.id === id)?.label ?? id;

/** The diff embedded in a standalone page, or null when this page is the interactive app. */
export function loadModel(): CompressedDiff | null {
  const element = document.getElementById("model");
  return JSON.parse(element?.textContent ?? "") as CompressedDiff | null;
}
