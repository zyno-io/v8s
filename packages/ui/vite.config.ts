import vue from '@vitejs/plugin-vue';
import { fileURLToPath, URL } from 'node:url';
import { defineConfig } from 'vitest/config';

export default defineConfig({
    // Absolute asset URLs: deep client routes (`/r/apps/v1/deployments`) must not resolve assets relative to themselves.
    // `v8s serve`, the desktop app:// origin and `vite preview` all serve the UI from the origin root with an SPA fallback.
    base: '/',
    server: { port: 5173, strictPort: false },
    preview: { port: 4173 },
    worker: { format: 'es' },
    build: { target: 'esnext', sourcemap: true },
    plugins: [vue()],
    resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
    test: { include: ['test/**/*.test.ts'] }
});
