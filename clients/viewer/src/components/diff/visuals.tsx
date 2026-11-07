import {
  Box, Braces, ChevronRight, Code2, FileCode, FileText, FunctionSquare, Hash, KeyRound, Layers, ListOrdered, ListTree,
  Minus, Package, Pencil, Plug, Plus, Shapes, Tag, Variable,
  type LucideIcon,
} from "lucide-react";
import { cn } from "@/lib/utils";
import type { ChangeOp, UnitKind } from "@/lib/model";

const OPS: Record<ChangeOp, { icon: LucideIcon; label: string; text: string; soft: string }> = {
  ADD: { icon: Plus, label: "ADD", text: "text-add", soft: "bg-add-soft" },
  CHANGE: { icon: Pencil, label: "CHANGE", text: "text-change", soft: "bg-change-soft" },
  REMOVE: { icon: Minus, label: "REMOVE", text: "text-remove", soft: "bg-remove-soft" },
};

const KINDS: Record<UnitKind, { icon: LucideIcon; label: string }> = {
  PACKAGE: { icon: Package, label: "Package" },
  STRUCT: { icon: Box, label: "Struct" },
  INTERFACE: { icon: Plug, label: "Interface" },
  TYPE: { icon: Box, label: "Type" },
  FUNCTION: { icon: FunctionSquare, label: "Function" },
  METHOD: { icon: Code2, label: "Method" },
  INTERFACE_METHOD: { icon: Code2, label: "Interface method" },
  FIELD: { icon: Tag, label: "Field" },
  CONST: { icon: Hash, label: "Constant" },
  VAR: { icon: Variable, label: "Variable" },
  FILE: { icon: FileText, label: "File" },
  REGION: { icon: ListTree, label: "Other lines" },
  MODULE: { icon: FileCode, label: "Module" },
  CLASS: { icon: Shapes, label: "Class" },
  ENUM: { icon: ListOrdered, label: "Enum" },
  ENUM_MEMBER: { icon: Hash, label: "Enum member" },
  TRAIT: { icon: Layers, label: "Trait" },
  RECORD: { icon: Braces, label: "Record" },
  PROPERTY: { icon: KeyRound, label: "Property" },
};

/** The operation in words and an icon, so color never carries the meaning alone. */
export function OpBadge({ op, className }: { op: ChangeOp; className?: string }) {
  const { icon: Icon, label, text, soft } = OPS[op];
  return (
    <span className={cn("inline-flex shrink-0 items-center gap-1 rounded-md px-2 py-0.5 text-xs font-semibold tracking-wide", text, soft, className)}>
      <Icon aria-hidden className="size-3" strokeWidth={3} />
      {label}
    </span>
  );
}

/** A round tinted icon. The surrounding text sets its color. */
export function IconMark({ icon: Icon, soft }: { icon: LucideIcon; soft: string }) {
  return (
    <span className={cn("flex size-7 shrink-0 items-center justify-center rounded-full", soft)}>
      <Icon aria-hidden className="size-4" strokeWidth={2.5} />
    </span>
  );
}

/** A large operation marker for headline cards. */
export function OpMark({ op }: { op: ChangeOp }) {
  const { icon: Icon, label, text, soft } = OPS[op];
  return (
    <span className={cn("inline-flex items-center gap-2 text-xs font-semibold tracking-wide", text)}>
      <IconMark icon={Icon} soft={soft} />
      {label}
    </span>
  );
}

export function KindIcon({ kind, className }: { kind: UnitKind; className?: string }) {
  const { icon: Icon, label } = KINDS[kind];
  return (
    <span title={label} className={cn("inline-flex shrink-0 text-muted-foreground", className)}>
      <Icon aria-hidden className="size-4" />
      <span className="sr-only">{label}</span>
    </span>
  );
}

/** The open or closed state, large enough to read at a glance. */
export function Chevron({ open }: { open: boolean }) {
  return (
    <span
      aria-hidden
      className="flex size-7 shrink-0 items-center justify-center rounded-md text-foreground transition-colors group-hover:bg-accent"
    >
      <ChevronRight className={cn("size-5 transition-transform duration-150", open && "rotate-90")} strokeWidth={3} />
    </span>
  );
}

export function LineStats({ added, removed }: { added: number; removed: number }) {
  if (!added && !removed) return null;
  return (
    <span className="shrink-0 font-mono text-xs whitespace-nowrap">
      <span className="text-add">+{added}</span> <span className="text-remove">−{removed}</span>
    </span>
  );
}
