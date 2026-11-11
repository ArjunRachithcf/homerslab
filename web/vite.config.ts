import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// The pages build into internal/surfaces/dist, which internal/surfaces/embed.go carries into the binary. design/ (the repository's design system)
// is imported from outside this folder, as the one source of the look.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/surfaces/dist',
    emptyOutDir: true,
    rollupOptions: { input: { setup: 'setup.html', account: 'account.html' } },
  },
  server: { fs: { allow: ['..'] } },
});
