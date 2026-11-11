// Placeholder links: replace them when the real destinations exist.

/** The version of the diffduck binary that served or wrote this page, or "dev" under Vite. */
function appVersion(): string {
  const v = document.querySelector('meta[name="diffduck-version"]')?.getAttribute("content") ?? "";
  return v && !v.startsWith("__") ? v : "dev";
}
const LINKS = ["Docs", "Changelog", "Report an issue", "Privacy"];

export function SiteFooter() {
  return (
    <footer data-testid="site-footer" className="border-t">
      <div className="mx-auto flex max-w-screen-2xl flex-col gap-4 px-4 py-6 text-sm text-muted-foreground md:flex-row md:items-center md:justify-between md:px-8">
        <div className="space-y-1">
          <p className="font-semibold text-foreground">DiffDuck</p>
          <p>Compressed, explorable Git diffs. Version <span data-testid="app-version">{appVersion()}</span>.</p>
        </div>
        <nav aria-label="Footer">
          <ul className="flex flex-wrap gap-x-6 gap-y-2">
            {LINKS.map((label) => (
              <li key={label}>
                <a href="#" className="underline-offset-4 hover:text-foreground hover:underline">{label}</a>
              </li>
            ))}
          </ul>
        </nav>
      </div>
    </footer>
  );
}
