import { defineConfig } from "vite";
import { viteSingleFile } from "vite-plugin-singlefile";

const input = process.env.INPUT;
if (!input) throw new Error("INPUT is required");

export default defineConfig({
  plugins: [viteSingleFile()],
  build: {
    rollupOptions: { input },
    outDir: "dist",
    emptyOutDir: true,
    minify: true,
  },
});
