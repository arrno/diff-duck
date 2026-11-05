import { test, expect, Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { pathToFileURL } from "node:url";
import { join } from "node:path";
import { readFileSync } from "node:fs";

const work = process.env.VERIFY_WORK as string;
const url = (run: string) => pathToFileURL(join(work, run, "index.html")).href;
const model = (run: string) => JSON.parse(readFileSync(join(work, run, "compressed-diff.json"), "utf8"));

async function open(page: Page, run = "normal") {
  const errors: string[] = [];
  page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto(url(run));
  return errors;
}

const node = (page: Page, id: string) => page.locator(`li[data-node-id="${id}"]`);
const usersID = "users|users";
const toggleOf = (page: Page, id: string) => page.locator(`button[data-toggle="${id}"]`);
const expand = async (page: Page, id: string) => {
  const toggle = toggleOf(page, id);
  if ((await toggle.getAttribute("aria-expanded")) === "false") await toggle.click();
};
const showService = async (page: Page) => {
  await expand(page, usersID);
  await expand(page, "users|users::Service");
};

test("the opening view shows the first level of what changed", async ({ page }) => {
  const errors = await open(page);
  await expect(page.getByTestId("overview")).toHaveText("Scripted overview of the change.");
  await expect(page.getByTestId("stats")).toContainText("13 files");
  await expect(page.getByTestId("stats")).toContainText("traced 63/63 changed lines");
  await expect(page.getByRole("button", { name: /All changes/ })).toHaveAttribute("aria-pressed", "true");
  await expect(page).toHaveTitle(/ · DiffDuck$/);
  await expect(page.locator('link[rel="icon"]')).toHaveCount(2);
  // A file tree: no level of it is a box.
  await expect(page.getByTestId("tree").locator('[data-slot="card"]')).toHaveCount(0);
  const boxed = await page.locator("li[data-node-id]").evaluateAll((rows) =>
    rows.filter((r) => parseFloat(getComputedStyle(r).borderTopWidth) > 0).length);
  expect(boxed).toBe(0);
  // Packages are open, so their contents show without a click, and types are still closed.
  await expect(node(page, usersID)).toContainText("Scripted parent of");
  await expect(toggleOf(page, usersID)).toHaveAttribute("aria-expanded", "true");
  await expect(node(page, "users|users::Service")).toBeVisible();
  await expect(node(page, "users|users::Service.Create")).toHaveCount(0);
  await expect(page.getByTestId("generation")).toContainText("scripted/normal");
  await expect(page.getByTestId("generation")).toContainText("does not verify");
  expect(errors).toEqual([]);
});

test("outcomes name the packages they touch", async ({ page }) => {
  await open(page);
  const card = page.getByRole("button", { name: /Behavior of billing/ });
  await expect(card).toContainText("CHANGE");
  await expect(card).toContainText("billing");
});

test("each package has a distinct accent color", async ({ page }) => {
  await open(page);
  const color = (id: string) => node(page, id).getByTestId("package-name").first().evaluate((e) => getComputedStyle(e).color);
  const colors = await Promise.all([usersID, "billing|billing", "legacy|legacy"].map(color));
  expect(new Set(colors).size).toBe(3);
});

test("rows with children say how many descendants they hold", async ({ page }) => {
  await open(page);
  const size = (n: { children?: unknown[] }): number =>
    ((n.children ?? []) as { children?: unknown[] }[]).reduce((sum, c) => sum + 1 + size(c), 0);
  const users = model("normal").nodes.find((n: { id: string }) => n.id === usersID);
  await expect(node(page, usersID).getByTestId("descendants").first()).toHaveText(`${size(users)} inside`);
  await expect(node(page, "users|users::Service").getByTestId("descendants")).toHaveText("1 inside");
  await expect(node(page, "users|users::ValidateDOB").getByTestId("descendants")).toHaveCount(0);
  // A filter counts only what it shows.
  type Unit = { id: string; outcomes?: string[]; children?: Unit[] };
  const m = model("normal");
  const id = m.outcomes.find((o: { title: string }) => o.title === "Behavior of users").id;
  await page.getByRole("button", { name: /All changes/ }).click();
  await page.getByRole("button", { name: /Behavior of users/ }).click();
  const shown = (n: Unit): boolean => (n.outcomes ?? []).includes(id) || (n.children ?? []).some(shown);
  const count = (n: Unit): number => (n.children ?? []).filter(shown).reduce((sum, c) => sum + 1 + count(c), 0);
  const filtered = count(users);
  expect(filtered).toBeLessThan(size(users));
  await expect(node(page, usersID).getByTestId("descendants").first()).toHaveText(`${filtered} inside`);
});

test("expand all and collapse all reach the whole tree", async ({ page }) => {
  await open(page);
  await expect(node(page, "users|users::Service.Create")).toHaveCount(0);
  await page.getByRole("button", { name: "Expand all" }).click();
  await expect(node(page, "users|users::Service.Create")).toBeVisible();
  await expect(node(page, "billing|billing::Cache.Get")).toBeVisible();
  await expect(page.locator('button[data-toggle][aria-expanded="false"]')).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Expand all" })).toBeFocused();
  await page.getByRole("button", { name: "Collapse all" }).click();
  await expect(node(page, usersID)).toBeVisible();
  await expect(node(page, "users|users::Service")).toHaveCount(0);
  await expect(page.locator('button[data-toggle][aria-expanded="true"]')).toHaveCount(0);
});

test("expanding reveals compressed children and never implementation", async ({ page }) => {
  await open(page);
  const service = node(page, "users|users::Service");
  await expect(service).toBeVisible();
  await expect(service).toContainText("inner changes only");
  await expect(node(page, "users|users::Service.Create")).toHaveCount(0);
  await expand(page, "users|users::Service");
  const create = node(page, "users|users::Service.Create");
  await expect(create).toContainText("Scripted leaf summary");
  // The signature waits until the source is asked for.
  await expect(create).not.toContainText("func (s *Service) Create(u User) error");
  await expect(create).toContainText("CHANGE");
  await expect(create).not.toContainText("ValidateDOB(u.DateOfBirth)");
  // Collapsing hides them again.
  await toggleOf(page, "users|users::Service").click();
  await expect(create).toHaveCount(0);
});

test("the raw diff of any node is reachable, with real lines in green and red", async ({ page }) => {
  await open(page);
  await showService(page);
  const create = node(page, "users|users::Service.Create");
  await create.getByRole("button", { name: "Show source of Service.Create" }).click();
  const added = create.locator('[data-kind="added"]').first();
  const removed = create.locator('[data-kind="removed"]').first();
  await expect(added).toBeVisible();
  await expect(removed).toBeVisible();
  const color = (l: typeof added) => l.evaluate((e) => getComputedStyle(e).color);
  expect(await color(added)).not.toEqual(await color(removed));
  await expect(create.getByTestId("source")).toContainText("ValidateDOB(u.DateOfBirth)");
  await expect(create.getByTestId("source")).toContainText("users/user.go");
  await expect(create).toContainText("func (s *Service) Create(u User) error");
  await create.getByRole("button", { name: "Hide source of Service.Create" }).click();
  await expect(create.getByTestId("source")).toHaveCount(0);
  // A type with no lines of its own still reaches what is inside it.
  await expand(page, "billing|billing");
  const cache = node(page, "billing|billing::Cache");
  await cache.getByRole("button", { name: "Show source of Cache" }).first().click();
  await expect(cache.getByTestId("source").first()).toContainText("var zero T");
});

test("the facts that matter are actually rendered", async ({ page }) => {
  await open(page);
  await expect(node(page, "users|users::addAndDouble")).toContainText("Was oldHelperName");
  await expect(node(page, "users|users::Relocated")).toContainText("moved");
  await expect(node(page, "file:users/broken.go").getByTestId("uncertainty")).toContainText("could not be parsed");
  await expand(page, "users|users::Service");
  const create = node(page, "users|users::Service.Create");
  // A semantic alias leads, and the real identity stays beside it.
  await expect(create).toContainText("createValidatedUser");
  await expect(create).toContainText("AKA Service.Create");
  // A directly changed unit does not claim only inner changes.
  await expect(create).not.toContainText("inner changes only");
  await expect(page.getByTestId("warnings")).toBeVisible();
  await expect(page.getByTestId("warnings")).toContainText("users/broken.go");
  await expect(node(page, "users|users::Low")).toContainText("Was: const Low = iota");
});

test("selecting an outcome filters with ancestors kept, and clearing restores", async ({ page }) => {
  const errors = await open(page);
  await expand(page, usersID);
  const before = await page.locator("li[data-node-id]").count();
  await page.getByRole("button", { name: /Behavior of billing/ }).click();
  await expect(page.getByTestId("filter-status")).toContainText("Behavior of billing");
  await expect(node(page, "billing|billing")).toBeVisible();
  // The leaf is visible, with its ancestors around it, without any manual expansion.
  await expect(node(page, "billing|billing::Cache.Get")).toBeVisible();
  await expect(node(page, usersID)).toHaveCount(0);
  await expect(node(page, "file:go.mod")).toHaveCount(0);
  await page.getByRole("button", { name: /All changes/ }).click();
  await expect(node(page, usersID)).toBeVisible();
  await expect(page.locator("li[data-node-id]")).toHaveCount(before);
  expect(errors).toEqual([]);
});

test("changes with no outcome stay reachable", async ({ page }) => {
  await open(page);
  const m = model("normal");
  await page.getByRole("button", { name: `No outcome (${m.unassigned.length})` }).click();
  await expect(node(page, "file:go.mod")).toBeVisible();
  await expect(node(page, "users|users::Service.Create")).toHaveCount(0);
});

test("repository content cannot execute in the page", async ({ page }) => {
  const errors = await open(page);
  await node(page, "file:odd dir/notes with space.md").getByRole("button", { name: /Show source/ }).click();
  await expect(node(page, "file:odd dir/notes with space.md")).toContainText("</script><script>window.__xss=1</script>");
  expect(await page.evaluate(() => (window as unknown as { __xss?: number }).__xss)).toBeUndefined();
  expect(errors).toEqual([]);
});

test("everything is operable by keyboard", async ({ page }) => {
  await open(page);
  const toggle = toggleOf(page, "users|users::Service");
  await toggle.focus();
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await page.keyboard.press("Enter");
  // Focus stays put, so a keyboard user does not lose their place.
  await expect(toggle).toBeFocused();
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await page.keyboard.press("Space");
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  const filter = page.getByRole("button", { name: /Behavior of billing/ });
  await filter.focus();
  await page.keyboard.press("Enter");
  await expect(filter).toBeFocused();
  await expect(filter).toHaveAttribute("aria-pressed", "true");
  // Tab order begins at the skip link, and reaches controls without a trap.
  await page.goto(url("normal"));
  await page.keyboard.press("Tab");
  await expect(page.getByRole("link", { name: "Skip to changed code" })).toBeFocused();
  const seen = new Set<string>();
  for (let i = 0; i < 40; i++) {
    await page.keyboard.press("Tab");
    seen.add(await page.evaluate(() => document.activeElement?.getAttribute("data-toggle") ?? document.activeElement?.getAttribute("data-filter") ?? ""));
  }
  expect(seen.has(usersID)).toBe(true);
});

for (const theme of ["light", "dark"] as const) {
  test(`no accessibility violations in ${theme} theme`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: theme });
    await open(page);
    await showService(page);
    await node(page, "users|users::Service.Create").getByRole("button", { name: /Show source/ }).click();
    await page.getByRole("button", { name: /Behavior of billing/ }).hover();
    const results = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21aa"]).analyze();
    expect(results.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target).join(", ")}`)).toEqual([]);
  });
}

test("no accessibility violations when every call failed", async ({ page }) => {
  await open(page, "failed");
  const results = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21aa"]).analyze();
  expect(results.violations.map((v) => v.id)).toEqual([]);
});

test("the theme follows the system, can be toggled, and persists", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "dark" });
  await open(page);
  const background = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  const dark = await background();
  await page.getByRole("button", { name: "Toggle theme" }).click();
  await expect.poll(background).not.toEqual(dark);
  const light = await background();
  await page.reload();
  await expect.poll(background).toEqual(light);
});

test("usable at 360px with no horizontal page scroll", async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 740 });
  await open(page);
  await showService(page);
  await node(page, "users|users::Service.Create").getByRole("button", { name: /Show source/ }).click();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(0);
});

test("a total model failure reads as unexplained, never as invented text", async ({ page }) => {
  const errors = await open(page, "failed");
  await expect(page.getByTestId("warnings")).toContainText("model call failed");
  await expect(page.getByTestId("overview")).toHaveCount(0);
  await expect(page.getByTestId("outcomes")).toContainText("No outcomes were identified.");
  await expect(node(page, usersID).getByTestId("no-summary").first()).toHaveText("No explanation available.");
  await expect(node(page, usersID).getByTestId("uncertainty").first()).toContainText("model call failed");
  expect(errors).toEqual([]);
});

test("an empty diff says so", async ({ page }) => {
  const errors = await open(page, "empty");
  await expect(page.getByTestId("tree")).toContainText("No changes between these commits.");
  expect(errors).toEqual([]);
});
