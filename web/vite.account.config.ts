import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// Admin's account page (first-time setup's last step), built on its own after the other pages, into the same folder,
// so it stays one file of its own (#115).
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/surfaces/dist',
    emptyOutDir: false,
    rollupOptions: { input: { account: 'account.html' } },
    modulePreload: { polyfill: false },
  },
  server: { fs: { allow: ['..'] } },
});
