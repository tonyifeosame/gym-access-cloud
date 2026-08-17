import { createReadStream, existsSync, statSync } from 'node:fs'
import { createServer } from 'node:http'
import { extname, join, normalize } from 'node:path'

/**
 * A static server for the built console, for the browser pass.
 *
 * Hand-written rather than a dependency: it needs to do exactly two things —
 * serve the files in dist/, and fall back to index.html for any path that is
 * not a file, because the console is a single-page application and its routes
 * exist only in the browser. A server that 404s /terminals/AT-0001 would make
 * every deep-link test fail for a reason that has nothing to do with the
 * console.
 *
 * It serves the REAL BUILD, not the dev server. That matters: the dev server
 * injects styles as inline <style> elements and does not apply the built
 * index.html, so a CSP check or a contrast check against it would be measuring
 * something that never ships.
 */

const TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.ico': 'image/x-icon',
  '.json': 'application/json; charset=utf-8',
  '.woff2': 'font/woff2',
}

export function serve(root, port = 0) {
  const server = createServer((request, response) => {
    const url = new URL(request.url ?? '/', 'http://localhost')
    // normalize collapses `..`, so a request cannot climb out of dist/.
    const requested = join(root, normalize(url.pathname))

    const path =
      requested.startsWith(root) && existsSync(requested) && statSync(requested).isFile()
        ? requested
        : join(root, 'index.html')

    response.writeHead(200, {
      'Content-Type': TYPES[extname(path)] ?? 'application/octet-stream',
      // No caching: a second run must not read the previous build.
      'Cache-Control': 'no-store',
    })
    createReadStream(path).pipe(response)
  })

  return new Promise((resolve) => {
    server.listen(port, '127.0.0.1', () => {
      const address = server.address()
      resolve({
        url: `http://127.0.0.1:${address.port}`,
        close: () => new Promise((done) => server.close(done)),
      })
    })
  })
}
