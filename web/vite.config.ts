import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist', emptyOutDir: true, sourcemap: false,
    rollupOptions: { output: { manualChunks: { react: ['react', 'react-dom', 'react-router-dom'] } } },
  },
  server: {
    port: 5173,
    proxy: { '/api': 'http://localhost:8080', '/healthz': 'http://localhost:8080', '/oidc': 'http://localhost:8080', '/.well-known': 'http://localhost:8080' },
  },
})
