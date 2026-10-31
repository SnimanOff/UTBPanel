import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// In development the API comes from `panel serve --dev` (TGPANEL_DEV=1):
// an HTTP listener without mTLS on 127.0.0.1:8080.
export default defineConfig({
  plugins: [svelte()],
  server: {
    host: '127.0.0.1',
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false },
    },
  },
  build: {
    target: 'es2022',
    assetsInlineLimit: 0,
  },
})
