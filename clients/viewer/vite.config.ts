import { readFileSync } from "node:fs";
import path from "node:path";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { viteSingleFile } from "vite-plugin-singlefile";

// Development only: fills the page from a saved compressed-diff.json, as duck-diff does at run time.
function devModel(): Plugin {
  return {
    name: "duck-diff-dev-model",
    apply: "serve",
    transformIndexHtml(html) {
      const file = process.env.DUCK_DIFF_JSON;
      if (!file) throw new Error("Set DUCK_DIFF_JSON to a compressed-diff.json, or run: make viewer-dev DIFF=path/to/compressed-diff.json");
      const model = JSON.stringify(JSON.parse(readFileSync(file, "utf8"))).replaceAll("<", "\\u003c");
      return html.replace("__DUCK_DIFF_MODEL__", () => model);
    },
  };
}

// One self-contained page: duck-diff embeds it and adds the model at run time.
export default defineConfig({
  plugins: [react(), tailwindcss(), viteSingleFile(), devModel()],
  resolve: { alias: { "@": path.resolve(__dirname, "src") } },
});
