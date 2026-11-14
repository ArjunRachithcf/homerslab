import react from '@vitejs/plugin-react';
import { readFileSync } from 'node:fs';
import { defineConfig, type Plugin } from 'vite';
import en from './src/i18n/en.json' with { type: 'json' };

// Home's own files, at the paths Home's process serves them (internal/home): its web app manifest, and the logo's
// icons from design/logo, the one source of them, so Home installs on a phone as a PWA (#21).
function homeFiles(): Plugin {
  const icons = ['icon-192.png', 'icon-512.png', 'maskable-512.png', 'favicon.svg', 'apple-touch-icon.png'];
  return {
    name: 'home-files',
    generateBundle() {
      for (const f of icons) this.emitFile({ type: 'asset', fileName: `home/${f}`, source: readFileSync(new URL(`../design/logo/${f}`, import.meta.url)) });
      const manifest = {
        name: en['home.manifest.name'], short_name: en['home.manifest.short'], description: en['home.manifest.description'],
        id: '/', start_url: '/', scope: '/', display: 'standalone', background_color: '#f5f5f7', theme_color: '#f5f5f7',
        icons: [
          { src: '/home/icon-192.png', sizes: '192x192', type: 'image/png' },
          { src: '/home/icon-512.png', sizes: '512x512', type: 'image/png' },
          { src: '/home/maskable-512.png', sizes: '512x512', type: 'image/png', purpose: 'maskable' },
          { src: '/home/favicon.svg', sizes: 'any', type: 'image/svg+xml' },
        ],
      };
      this.emitFile({ type: 'asset', fileName: 'home/manifest.webmanifest', source: `${JSON.stringify(manifest, null, 2)}\n` });
    },
  };
}

// The pages build into internal/surfaces/dist (Home, with its own files; Admin's account page, first-time setup's last step, with its own build,
// vite.account.config.ts), which internal/surfaces/embed.go carries into the binary. design/ (the repository's design
// system) is imported from outside this folder, as the one source of the look.
export default defineConfig({
  plugins: [react(), homeFiles()],
  build: {
    outDir: '../internal/surfaces/dist',
    emptyOutDir: true,
    rollupOptions: { input: { setup: 'setup.html', admin: 'admin.html', home: 'home.html' } },
    // No dynamic imports, so there is nothing to preload: without the polyfill, none of vite's own code
    // reaches the bundle (#115; e2e/bundle.spec.ts checks it).
    modulePreload: { polyfill: false },
  },
  server: { fs: { allow: ['..'] } },
});
