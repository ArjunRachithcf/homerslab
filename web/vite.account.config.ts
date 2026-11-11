import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// Admin's account page (first-time setup's last step), built on its own after setup's page, into the same folder: two
// entries in one build would share chunks, and each page is one file (#115).
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
