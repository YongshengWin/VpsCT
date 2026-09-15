import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";
import { mkdirSync, writeFileSync } from "node:fs";

export default defineConfig({
  plugins: [react(), {
    name: "keep-go-embed-placeholder",
    closeBundle() {
      const dist = path.resolve(import.meta.dirname, "dist");
      mkdirSync(dist, { recursive: true });
      writeFileSync(path.join(dist, ".gitkeep"), "\n");
    },
  }],
  resolve: { alias: { "@": path.resolve(import.meta.dirname, "src") } },
  server: {
    port: 5173,
    proxy: {
      "/api": { target: "http://127.0.0.1:8080", changeOrigin: false },
      "/s": "http://127.0.0.1:8080",
      "/r": "http://127.0.0.1:8080",
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
    rolldownOptions: {
      output: {
        manualChunks(id) {
          if (/node_modules\/(react|react-dom|react-router|react-router-dom|scheduler)\//.test(id)) return "react";
          if (/node_modules\/recharts\//.test(id)) return "charts";
          if (/node_modules\/@tanstack\//.test(id)) return "query";
        },
      },
    },
  },
});
