import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { defineConfig } from 'vite'
import YAML from 'yaml'

/*
 * The OpenAPI document lives at apidocs/openapi.yaml -- one file, the single
 * source for the whole site -- and is NOT under public/, so nothing else gets
 * a second copy to drift. This plugin serves it at /openapi.yaml (and a JSON
 * rendering at /openapi.json) in development, and emits both into the build.
 */
function openapiDocument() {
  const source = resolve(import.meta.dirname, 'openapi.yaml')
  const read = () => readFileSync(source, 'utf8')
  const asJSON = (text) => JSON.stringify(YAML.parse(text), null, 2)

  return {
    name: 'accesslink-openapi-document',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const path = (req.url ?? '').split('?')[0]
        if (path === '/openapi.yaml') {
          res.setHeader('Content-Type', 'application/yaml; charset=utf-8')
          res.end(read())
          return
        }
        if (path === '/openapi.json') {
          res.setHeader('Content-Type', 'application/json; charset=utf-8')
          res.end(asJSON(read()))
          return
        }
        next()
      })
    },
    generateBundle() {
      const text = read()
      this.emitFile({ type: 'asset', fileName: 'openapi.yaml', source: text })
      this.emitFile({ type: 'asset', fileName: 'openapi.json', source: asJSON(text) })
    },
  }
}

export default defineConfig({
  base: '/',
  plugins: [openapiDocument()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    // Scalar is one large bundle by nature; the warning is noise here.
    chunkSizeWarningLimit: 3000,
  },
  server: { port: 5175, strictPort: true },
  preview: { port: 5176, strictPort: true },
})
