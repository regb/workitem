import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import type { ProxyOptions } from 'vite'
import { defineConfig } from 'vitest/config'

// The build lands in internal/web/dist/app, which the wi binary embeds.
// `npm run dev` proxies the API to a running `wi web` (WI_WEB_URL), as that
// server's own origin: it refuses requests for another Host or Origin.
const target = process.env.WI_WEB_URL ?? 'http://127.0.0.1:8787'
const toWiWeb: ProxyOptions = {
  target,
  changeOrigin: true,
  configure: proxy => proxy.on('proxyReq', request => request.getHeader('origin') && request.setHeader('origin', new URL(target).origin)),
}

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
  // Keep Pierre's worker imports split instead of inlining every grammar.
  worker: { format: 'es' },
  server: {
    // The page's API calls and its media URLs both go to wi web, which
    // checks that a call comes from its own origin.
    proxy: { '/api': toWiWeb, '/wi.core.v1.': toWiWeb },
  },
  test: { environment: 'jsdom', include: ['src/**/*.test.{ts,tsx}'] },
})
