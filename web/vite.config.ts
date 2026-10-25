import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

// The build lands in internal/web/dist/app, which the wi binary embeds.
// `npm run dev` proxies the API to a running `wi web` (WI_WEB_URL), as that
// server's own origin: it refuses requests for another Host or Origin.
const target = process.env.WI_WEB_URL ?? 'http://127.0.0.1:8787'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { '@': path.resolve(import.meta.dirname, 'src') } },
  build: {
    outDir: path.resolve(import.meta.dirname, '../internal/web/dist/app'),
    emptyOutDir: true,
    sourcemap: false,
    // React, Radix and the router form one ~180 kB gzip chunk; the chat page
    // (Markdown, highlighting) loads separately on first use.
    chunkSizeWarningLimit: 800,
  },
  server: {
    proxy: {
      '/api': {
        target,
        changeOrigin: true,
        configure: proxy => proxy.on('proxyReq', request => request.getHeader('origin') && request.setHeader('origin', new URL(target).origin)),
      },
    },
  },
  test: { environment: 'jsdom', include: ['src/**/*.test.{ts,tsx}'] },
})
