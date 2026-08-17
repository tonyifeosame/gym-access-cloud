import { defineConfig, type Plugin } from 'vitest/config'
import react from '@vitejs/plugin-react'

import { buildCsp } from './src/security/csp'

/**
 * Emits the Content Security Policy into index.html at build time.
 *
 * A PLUGIN RATHER THAN A LITERAL IN THE HTML because one directive depends on
 * the deployment: `connect-src` has to name the API's origin, which is
 * VITE_API_BASE_URL and differs between the split-origin production deployment
 * and the same-origin development proxy. A hard-coded policy would either be
 * wrong in development or too loose in production.
 *
 * The policy itself lives in src/security/csp.ts, where it is documented
 * directive by directive and covered by tests. See the note there on why this
 * ships as a meta tag AND should be repeated as a header once the console has a
 * deployment configuration -- `frame-ancestors` cannot be expressed here.
 */
function cspPlugin(mode: string): Plugin {
  return {
    name: 'accesslink-csp',
    transformIndexHtml(html) {
      const policy = buildCsp({
        apiBaseUrl: process.env.VITE_API_BASE_URL,
        development: mode !== 'production',
      })
      return html.replace(
        '</head>',
        `  <meta http-equiv="Content-Security-Policy" content="${policy}" />\n  </head>`,
      )
    },
  }
}

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
export default defineConfig(({ mode }) => ({
  plugins: [react(), cspPlugin(mode)],
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
}))
