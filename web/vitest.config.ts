import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// The shared sources are tested against test-provider/, a made-up provider
// standing in for a plugin's web/src/provider.
export default defineConfig({
  resolve: {
    alias: {
      "@core": fileURLToPath(new URL("./src", import.meta.url)),
      "@provider": fileURLToPath(new URL("./test-provider", import.meta.url)),
    },
  },
  plugins: [react()],
  test: {
    environment: "jsdom",
    globals: true,
    include: ["src/**/*.{test,spec}.{ts,tsx}", "test-provider/**/*.{test,spec}.{ts,tsx}"],
  },
});
