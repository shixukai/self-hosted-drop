import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
// Keep browser requests same-origin during local HMR; only local development uses this rewrite.
const proxy = {
  target: "http://localhost:18180",
  changeOrigin: true,
  configure(server: any) {
    server.on("proxyReq", (req: any) => {
      req.setHeader("Origin", "http://localhost:18180");
    });
  },
};
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: { host: "127.0.0.1", proxy: { "/api": proxy, "/health": proxy } },
  build: { sourcemap: false },
});
