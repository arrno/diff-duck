import { createContext, useContext } from "react";
import type { Filter } from "@/lib/tree";

/** Presentation state shared across the tree. It never alters the model. */
export interface ViewState {
  filter: Filter;
  unassigned: Set<string>;
  expanded: Set<string>;
  toggle: (id: string) => void;
  source: Set<string>;
  toggleSource: (id: string) => void;
}

export const ViewContext = createContext<ViewState | null>(null);

export function useView(): ViewState {
  const view = useContext(ViewContext);
  if (!view) throw new Error("useView needs a ViewContext provider");
  return view;
}
