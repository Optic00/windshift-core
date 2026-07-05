import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [svelte()],
  build: {
    sourcemap: false,
    outDir: 'dist/embed',
    emptyOutDir: false,
    lib: {
      entry: 'src/embed/index.js',
      name: 'WindshiftForms',
      formats: ['es', 'iife'],
      fileName: (format) => (format === 'es' ? 'windshift-forms.es.js' : 'windshift-forms.js'),
    },
    rolldownOptions: {
      output: {
        exports: 'named',
      },
    },
  },
});
