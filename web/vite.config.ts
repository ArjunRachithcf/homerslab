import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// The pages build into internal/surfaces/dist (Admin's account page, first-time setup's last step, with its own build,
// vite.account.config.ts), which internal/surfaces/embed.go carries into the binary. design/ (the repository's design
// system) is imported from outside this folder, as the one source of the look.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/surfaces/dist',
    emptyOutDir: true,
    rollupOptions: { input: { setup: 'setup.html', admin: 'admin.html' } },
    // No dynamic imports, so there is nothing to preload: without the polyfill, none of vite's own code
    // reaches the bundle (#115; e2e/bundle.spec.ts checks it).
    modulePreload: { polyfill: false },
  },
  server: { fs: { allow: ['..'] } },
});
