import vue from '@vitejs/plugin-vue';
import { fileURLToPath, URL } from 'node:url';
import { defineConfig } from 'vitest/config';

export default defineConfig({
    // Relative asset URLs so the same build loads from `v8s serve`, a file/app:// origin, and `vite preview`.
    base: './',
    server: { port: 5173, strictPort: false },
    preview: { port: 4173 },
    worker: { format: 'es' },
    build: { target: 'esnext', sourcemap: true },
    plugins: [vue()],
    resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
    test: { include: ['test/**/*.test.ts'] }
});
