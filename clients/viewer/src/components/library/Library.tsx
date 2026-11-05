import { useEffect, useState, type ReactNode } from "react";
import { Plus } from "lucide-react";
import { Navbar } from "@/components/Navbar";
import { SiteFooter } from "@/components/SiteFooter";
import { Button } from "@/components/ui/button";
import { DiffList } from "./DiffList";
import { DiffPage } from "./DiffPage";
import { NewDiff } from "./NewDiff";

export const HOME = "#/";
export const NEW_DIFF = "#/new";
export const diffHref = (id: string) => `#/diffs/${encodeURIComponent(id)}`;

function useHash(): string {
  const [hash, setHash] = useState(window.location.hash);
  useEffect(() => {
    const update = () => setHash(window.location.hash);
    window.addEventListener("hashchange", update);
    return () => window.removeEventListener("hashchange", update);
  }, []);
  return hash;
}

/** Names the tab after the current page, or modal, until it goes away. */
export function usePageTitle(title: string) {
  useEffect(() => {
    const previous = document.title;
    document.title = `${title} · DiffDuck`;
    return () => { document.title = previous; };
  }, [title]);
}

export function NewDiffLink() {
  return (
    <Button asChild size="sm">
      <a href={NEW_DIFF}><Plus aria-hidden />New diff</a>
    </Button>
  );
}

export function Page({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-screen flex-col">
      <Navbar home={HOME} />
      <main className="mx-auto w-full max-w-4xl flex-1 space-y-6 px-4 py-8 md:px-8">{children}</main>
      <SiteFooter />
    </div>
  );
}

/** The app: a saved-diff list with a modal for a new diff, and one diff at a time. */
export function Library() {
  const hash = useHash();
  useEffect(() => { window.scrollTo(0, 0); }, [hash]);
  const match = hash.match(/^#\/diffs\/(.+)$/);
  if (match) return <DiffPage key={match[1]} id={decodeURIComponent(match[1])} />;
  return (
    <>
      <DiffList />
      {hash === NEW_DIFF && <NewDiff />}
    </>
  );
}
