import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';

export default defineConfig({
  plugins: [vue()],
  // Build straight into ../web, which the Go binary embeds.
  build: { outDir: '../web', emptyOutDir: true },
  // `npm run dev` serves the UI with hot reload; API calls go to the Go server.
  server: { proxy: { '/api': process.env.API_URL ?? 'http://127.0.0.1:8080' } },
});
