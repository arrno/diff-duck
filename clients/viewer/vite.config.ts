import { readFileSync } from "node:fs";
import path from "node:path";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { viteSingleFile } from "vite-plugin-singlefile";

// Development only: fills the page from a saved compressed-diff.json, as diff-duck does at run
// time. Without one, the page is the app, talking to `diffduck serve` through the proxy below.
function devModel(): Plugin {
  return {
    name: "diff-duck-dev-model",
    apply: "serve",
    transformIndexHtml(html) {
      const file = process.env.DIFF_DUCK_JSON;
      if (!file) return html.replace("__DIFF_DUCK_MODEL__", "null");
      const model = JSON.stringify(JSON.parse(readFileSync(file, "utf8"))).replaceAll("<", "\\u003c");
      return html.replace("__DIFF_DUCK_MODEL__", () => model);
    },
  };
}

// One self-contained page: diff-duck embeds it and adds the model at run time.
export default defineConfig({
  plugins: [react(), tailwindcss(), viteSingleFile(), devModel()],
  server: {
    proxy: {
      "/api": { target: "http://127.0.0.1:7474", changeOrigin: true, headers: { Origin: "http://127.0.0.1:7474" } },
    },
  },
  resolve: { alias: { "@": path.resolve(__dirname, "src") } },
});
