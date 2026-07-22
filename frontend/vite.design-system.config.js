import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

const frontendRoot = fileURLToPath(new URL('.', import.meta.url));

export default defineConfig({
  base: './',
  plugins: [svelte(), tailwindcss()],
  server: {
    // Workspace edits can bypass native filesystem events. Polling keeps the
    // standalone viewer's module graph in sync with the files on disk.
    watch: {
      usePolling: true,
      interval: 100,
    },
  },
  build: {
    outDir: 'dist-design-system',
    emptyOutDir: true,
    assetsDir: '_app',
    rolldownOptions: {
      input: resolve(frontendRoot, 'design-system.html'),
    },
  },
});
