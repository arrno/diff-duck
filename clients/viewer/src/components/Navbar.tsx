import { Moon, Sun } from "lucide-react";
import { useTheme } from "next-themes";
import { Button } from "@/components/ui/button";
import { ProviderMenu } from "@/components/ProviderMenu";

/** No border or shadow. The background darkens on hover and while a menu is open. */
export const navIconClass = "hover:bg-accent data-[state=open]:bg-accent dark:hover:bg-accent";

function ThemeToggle() {
  const { resolvedTheme, setTheme } = useTheme();
  const dark = resolvedTheme === "dark";
  return (
    <Button type="button" variant="ghost" size="icon" className={navIconClass} aria-label="Toggle theme" onClick={() => setTheme(dark ? "light" : "dark")}>
      {dark ? <Sun aria-hidden /> : <Moon aria-hidden />}
    </Button>
  );
}

function DuckIcon() {
  return (
    <svg aria-hidden viewBox="0 0 32 32" className="size-7">
      <path fill="#FBBF24" d="M3 13c1.5 3 4.5 4.2 8.5 4L20 17c3.5 0 6.5 2.2 6.5 5.2 0 3.6-4 6-9.5 6-7.5 0-13.5-4.2-14-15.2z" />
      <circle cx="20" cy="11" r="6.5" fill="#FBBF24" />
      <path fill="#F97316" d="M26 8.8 31.5 11 26 13.8z" />
      <circle cx="21.8" cy="9.5" r="1.2" fill="#1C1917" />
      <path fill="none" stroke="#D97706" strokeLinecap="round" strokeWidth="1.8" d="M9.5 20.5c3 3 7.5 3 10.5-.3" />
    </svg>
  );
}

/** `home` makes the name a link back to the start of the app. */
export function Navbar({ home }: { home?: string }) {
  const brandClass = "flex items-center gap-2 text-base font-semibold tracking-tight";
  const brand = <><DuckIcon />DiffDuck</>;
  return (
    <header className="sticky top-0 z-50 border-b bg-background/85 backdrop-blur">
      <div className="mx-auto flex h-14 max-w-screen-2xl items-center justify-between gap-4 px-4 md:px-8">
        {home ? <a href={home} className={brandClass}>{brand}</a> : <span className={brandClass}>{brand}</span>}
        <div className="flex items-center gap-2">
          <ThemeToggle />
          {/* Only the app has settings. A standalone page is a file, with nowhere to keep them. */}
          {home && (
            <>
              <span aria-hidden className="h-5 w-px bg-border" />
              <ProviderMenu />
            </>
          )}
        </div>
      </div>
    </header>
  );
}
