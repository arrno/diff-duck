/** A commit reference short enough to read: full hashes shrink, branch names stay. */
export function shortRef(ref: string): string {
  return /^[0-9a-f]{40,64}$/i.test(ref) ? ref.slice(0, 8) : ref;
}

/** The last part of a path or URL, which is how people name a repository. */
export function repositoryName(repository: string): string {
  return repository.replace(/\/+$/, "").split(/[/:]/).pop()?.replace(/\.git$/, "") || repository;
}

const units: [Intl.RelativeTimeFormatUnit, number][] = [["year", 31536000], ["month", 2592000], ["day", 86400], ["hour", 3600], ["minute", 60]];
const relative = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

export function timeAgo(iso: string, now = Date.now()): string {
  const seconds = (new Date(iso).getTime() - now) / 1000;
  for (const [unit, size] of units) {
    if (Math.abs(seconds) >= size) return relative.format(Math.round(seconds / size), unit);
  }
  return "just now";
}

export function elapsed(fromIso: string, now = Date.now()): string {
  const total = Math.max(0, Math.round((now - new Date(fromIso).getTime()) / 1000));
  return total < 60 ? `${total}s` : `${Math.floor(total / 60)}m ${total % 60}s`;
}
