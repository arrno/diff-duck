import type { CSSProperties } from "react";

const PALETTE_SIZE = 8;

function hash(text: string): number {
  let h = 0;
  for (const c of text) h = (h * 31 + c.charCodeAt(0)) | 0;
  return Math.abs(h);
}

/**
 * Gives each package label a color. The hash keeps "controller" the same color from diff to
 * diff, and probing keeps labels on one page apart until the palette runs out.
 */
export function assignPackageColors(labels: string[]): Map<string, number> {
  const colors = new Map<string, number>();
  const used = new Set<number>();
  for (const label of [...new Set(labels)].sort()) {
    let slot = hash(label) % PALETTE_SIZE;
    for (let i = 0; i < PALETTE_SIZE && used.has(slot); i++) slot = (slot + 1) % PALETTE_SIZE;
    used.add(slot);
    if (used.size === PALETTE_SIZE) used.clear();
    colors.set(label, slot);
  }
  return colors;
}

export function packageStyle(slot: number | undefined): CSSProperties {
  return { "--pkg": `var(--pkg-${slot ?? 0})` } as CSSProperties;
}
