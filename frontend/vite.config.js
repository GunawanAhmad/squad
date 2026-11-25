import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';

export default defineConfig({
  plugins: [vue()],
  // Build straight into ../web, which the Go binary embeds.
  build: { outDir: '../web', emptyOutDir: true },
  // `make dev` serves the UI through the Go server (which proxies to this
  // port), so keep the port fixed. HMR connects back to the Go server on the
  // page's own port; `npm run dev` alone still serves :5173 with /api proxied.
  server: {
    host: '127.0.0.1',
    strictPort: true,
    proxy: { '/api': process.env.API_URL ?? 'http://127.0.0.1:8080' },
  },
});
