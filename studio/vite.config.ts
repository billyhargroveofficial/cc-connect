import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
export default defineConfig({
  plugins: [react()],
  server: {
    host: "0.0.0.0",
    port: 5173,
    strictPort: true,
    proxy: {
      "/api/studio": {
        target: "http://127.0.0.1:9830",
        changeOrigin: false,
        timeout: 120000,
      },
    },
  },
});
