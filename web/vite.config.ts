import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// Setup's page builds into internal/surfaces/dist (Admin's account page with its own build, vite.account.config.ts, so
// that each page stays one file), which internal/surfaces/embed.go carries into the binary. design/ (the repository's design system)
// is imported from outside this folder, as the one source of the look.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/surfaces/dist',
    emptyOutDir: true,
    rollupOptions: { input: { setup: 'setup.html' } },
    // No module-preload polyfill, so none of vite's own code reaches the bundle (#115; e2e/bundle.spec.ts checks it).
    modulePreload: { polyfill: false },
  },
  server: { fs: { allow: ['..'] } },
});
