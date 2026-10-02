import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export const publicHosts = ["billyhargrove.ru"];

const workspaceProxy = {
  "/api/studio": {
    target: "http://127.0.0.1:9830",
    changeOrigin: false,
    ws: true,
    timeout: 120000,
  },
};

export default defineConfig({
  plugins: [react()],
  server: {
    host: "0.0.0.0",
    port: 5173,
    strictPort: true,
    allowedHosts: publicHosts,
    proxy: workspaceProxy,
  },
  preview: {
    host: "0.0.0.0",
    port: 5173,
    strictPort: true,
    allowedHosts: publicHosts,
    proxy: workspaceProxy,
  },
});
