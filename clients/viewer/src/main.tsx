import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { ThemeProvider } from "next-themes";
import { App } from "./App";
import { Library } from "./components/library/Library";
import { loadModel } from "./lib/model";
import "./index.css";

const model = loadModel();

createRoot(document.getElementById("root") as HTMLElement).render(
  <StrictMode>
    <ThemeProvider attribute="class" defaultTheme="system" enableSystem>
      {model ? <App model={model} /> : <Library />}
    </ThemeProvider>
  </StrictMode>,
);
