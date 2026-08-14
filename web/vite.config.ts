import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// Development proxies /api to the Go server, which makes the dashboard and the
// API the SAME ORIGIN locally. Two consequences worth knowing:
//
//   * No CORS is involved at all in development, so CORS_ALLOWED_ORIGINS does
//     not need to be set to work locally -- and a CORS mistake cannot be
//     discovered here. It has to be verified against the real split-origin
//     deployment.
//   * The session cookie is Secure, and Chrome and Firefox accept Secure
//     cookies on http://localhost because they treat it as a trustworthy
//     origin. Safari does not; set SESSION_COOKIE_INSECURE=1 on the API if you
//     develop there.
//
// In production the dashboard is served from app.accesslink.store and talks to
// api.accesslink.store via VITE_API_BASE_URL. They share a registrable domain,
// so the SameSite=Lax cookie is sent; CORS allows the cross-origin read.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: process.env.VITE_DEV_API_TARGET ?? 'http://localhost:8080',
        changeOrigin: false,
      },
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    css: false,
    restoreMocks: true,
  },
})
