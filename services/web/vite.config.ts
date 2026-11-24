import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// `npm run dev` proxies /api to a portfolio-api started separately (API_URL, default http://127.0.0.1:8081).
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: { "/api": process.env.API_URL ?? "http://127.0.0.1:8081" },
  },
});
