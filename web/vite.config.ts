import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// Dev server proxies API traffic to the Go backend. The port is configurable
// through RAPIG_API_ORIGIN so a local conflict on :8080 can be avoided.
const backend = process.env.RAPIG_API_ORIGIN ?? 'http://127.0.0.1:8080'

export default defineConfig({
  base: '/',
  plugins: [vue()],
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      '/api': { target: backend, changeOrigin: true },
      '/health': { target: backend, changeOrigin: true },
    },
  },
  build: {
    outDir: 'dist',
    // build.sh clears dist/ and drops a .keep file; never wipe it.
    emptyOutDir: false,
  },
})
