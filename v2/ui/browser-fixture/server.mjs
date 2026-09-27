import { createServer } from 'node:http'
import { readFile, stat } from 'node:fs/promises'
import { dirname, extname, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

const LOOPBACK = '127.0.0.1'
const FIXTURE_VERSION = 'norn.ui-retry-fixture/v1'
const DEFAULT_DIST = resolve(dirname(fileURLToPath(import.meta.url)), '..', 'dist')
const BODY_LIMIT = 64 * 1024

const contentTypes = new Map([
  ['.css', 'text/css; charset=utf-8'],
  ['.html', 'text/html; charset=utf-8'],
  ['.js', 'text/javascript; charset=utf-8'],
  ['.json', 'application/json; charset=utf-8'],
  ['.svg', 'image/svg+xml'],
  ['.woff', 'font/woff'],
  ['.woff2', 'font/woff2'],
])

function freshState() {
  return {
    fixtureVersion: FIXTURE_VERSION,
    preflight: { attempts: 0, keys: [], keyMismatch: false },
    deploy: { attempts: 0, keys: [] },
    operationPolls: 0,
    deployGroup: { attempts: 0, keys: [], keyMismatch: false },
    unexpectedApi: [],
  }
}

function sendJSON(response, status, value) {
  const body = JSON.stringify(value)
  response.writeHead(status, {
    'Cache-Control': 'no-store',
    'Content-Length': Buffer.byteLength(body),
    'Content-Type': 'application/json; charset=utf-8',
    'X-Norn-Fixture': FIXTURE_VERSION,
  })
  response.end(body)
}

async function readJSON(request) {
  const chunks = []
  let size = 0
  for await (const chunk of request) {
    size += chunk.length
    if (size > BODY_LIMIT) throw new Error('request body exceeds fixture limit')
    chunks.push(chunk)
  }
  const body = Buffer.concat(chunks).toString('utf8')
  return body ? JSON.parse(body) : {}
}

function idempotencyKey(request) {
  const value = request.headers['idempotency-key']
  return typeof value === 'string' ? value : ''
}

function syntheticApp() {
  return {
    spec: {
      name: 'api',
      deploy: true,
      processes: { web: { port: 8800, health: { path: '/health' } } },
      endpoints: [],
      repo: { url: 'git@github.com:fixture/api.git' },
    },
    nomadStatus: 'running',
    healthy: true,
    allocations: [{ id: 'fixture-allocation', taskGroup: 'web', status: 'running', lifecycle: 'active' }],
    allocationSummary: { running: 1, active: 1, retained: 0, total: 1 },
  }
}

function platformSummary() {
  return {
    generatedAt: '2026-09-22T12:00:00Z',
    networkMode: 'fixture',
    services: { total: 1, public: 0, private: 1, local: 0, internal: 0, byType: { web: 1 }, byStatus: { passing: 1 } },
    deployments: { recent: [], dirty: [], failed: 0, successful: 0 },
    access: { totalRecent: 0, byStatus: {}, byClientIp: {}, recent: [] },
    observability: { enabled: false, logsEnabled: false, logFormat: 'json' },
  }
}

function routeAPI(request, response, pathname, state) {
  const method = request.method ?? 'GET'

  if (method === 'POST' && pathname === '/api/apps/api/preflight') {
    return readJSON(request).then((body) => {
      const key = idempotencyKey(request)
      if (!key || body.ref !== 'HEAD') return sendJSON(response, 400, { error: 'fixture requires HEAD and Idempotency-Key' })
      state.preflight.attempts += 1
      state.preflight.keys.push(key)
      if (state.preflight.keys.some((candidate) => candidate !== state.preflight.keys[0])) {
        state.preflight.keyMismatch = true
        return sendJSON(response, 409, { error: 'fixture detected a changed preflight request key' })
      }
      if (state.preflight.attempts === 1) {
        // Model an accepted request whose response never reaches the browser.
        request.socket.destroy()
        return
      }
      if (state.preflight.attempts === 2) return sendJSON(response, 401, { error: 'fixture session expired after unknown outcome' })
      return sendJSON(response, 200, {
        sagaId: 'fixture-preflight-saga',
        operationId: 'fixture-preflight-operation',
        status: 'succeeded',
        replayed: true,
      })
    })
  }

  if (method === 'POST' && pathname === '/api/apps/api/deploy') {
    return readJSON(request).then((body) => {
      const key = idempotencyKey(request)
      if (!key || body.ref !== 'HEAD') return sendJSON(response, 400, { error: 'fixture requires HEAD and Idempotency-Key' })
      state.deploy.attempts += 1
      state.deploy.keys.push(key)
      return sendJSON(response, 202, {
        sagaId: 'fixture-deploy-saga',
        operationId: 'fixture-poll-operation',
        status: 'running',
      })
    })
  }

  if (method === 'GET' && pathname === '/api/v1/operations/fixture-poll-operation') {
    state.operationPolls += 1
    if (state.operationPolls === 1) return sendJSON(response, 503, { error: 'fixture transient poll failure' })
    return sendJSON(response, 200, { id: 'fixture-poll-operation', status: 'succeeded' })
  }

  if (method === 'POST' && pathname === '/api/deploy-groups/core/deploy') {
    return readJSON(request).then((body) => {
      const key = idempotencyKey(request)
      if (!key || body.ref !== 'HEAD') return sendJSON(response, 400, { error: 'fixture requires HEAD and Idempotency-Key' })
      state.deployGroup.attempts += 1
      state.deployGroup.keys.push(key)
      if (state.deployGroup.keys.some((candidate) => candidate !== state.deployGroup.keys[0])) {
        state.deployGroup.keyMismatch = true
        return sendJSON(response, 409, { error: 'fixture detected a changed parent request key' })
      }
      if (state.deployGroup.attempts === 1) {
        return sendJSON(response, 202, {
          group: 'core',
          operationId: 'fixture-group-operation',
          deploys: [
            { app: 'api', sagaId: 'fixture-group-api-saga', operationId: 'fixture-group-api-operation' },
            { app: 'worker', error: 'fixture worker unavailable' },
          ],
        })
      }
      return sendJSON(response, 200, {
        group: 'core',
        operationId: 'fixture-group-operation',
        replayed: true,
        deploys: [
          { app: 'api', sagaId: 'fixture-group-api-saga', operationId: 'fixture-group-api-operation', replayed: true },
          { app: 'worker', sagaId: 'fixture-group-worker-saga', operationId: 'fixture-group-worker-operation' },
        ],
      })
    })
  }

  if (method === 'GET' && pathname === '/api/apps') return sendJSON(response, 200, [syntheticApp()])
  if (method === 'GET' && pathname === '/api/apps/api/canary') return sendJSON(response, 200, null)
  if (method === 'GET' && pathname === '/api/services/manifest') return sendJSON(response, 200, { version: 1, generatedAt: '2026-09-22T12:00:00Z', networkMode: 'fixture', services: [] })
  if (method === 'GET' && pathname === '/api/access/patterns') return sendJSON(response, 200, { windowHours: 24, idleAfterHours: 72, patterns: [] })
  if (method === 'GET' && pathname === '/api/v1/capabilities') return sendJSON(response, 200, { protocolVersion: 1, serverVersion: 'browser-fixture', features: [] })
  if (method === 'GET' && pathname === '/api/cloudflared/ingress') return sendJSON(response, 200, { hostnames: [] })
  if (method === 'GET' && pathname === '/api/version') return sendJSON(response, 200, { version: 'browser-fixture' })
  if (method === 'GET' && pathname === '/api/health') return sendJSON(response, 200, { status: 'ok', services: { fixture: 'up' } })
  if (method === 'GET' && pathname === '/api/ops/platform') return sendJSON(response, 200, platformSummary())
  if (method === 'GET' && pathname === '/api/platform/releases') return sendJSON(response, 200, { current: 'fixture', releases: [] })
  if (method === 'GET' && pathname === '/api/deploy-groups') {
    return sendJSON(response, 200, { groups: [{ name: 'core', apps: [{ app: 'api' }, { app: 'worker', waitReady: true }] }] })
  }

  state.unexpectedApi.push(`${method} ${pathname}`)
  return sendJSON(response, 404, { error: 'unsupported synthetic fixture endpoint', method, path: pathname })
}

async function serveStatic(response, pathname, distDir) {
  let relativePath
  try {
    relativePath = decodeURIComponent(pathname).replace(/^\/+/, '')
  } catch {
    return sendJSON(response, 400, { error: 'invalid URL encoding' })
  }
  if (!relativePath || (!relativePath.startsWith('assets/') && extname(relativePath) === '')) relativePath = 'index.html'
  const candidate = resolve(distDir, relativePath)
  if (candidate !== distDir && !candidate.startsWith(`${distDir}${sep}`)) return sendJSON(response, 404, { error: 'fixture file not found' })
  try {
    const info = await stat(candidate)
    if (!info.isFile()) throw new Error('not a file')
    const body = await readFile(candidate)
    response.writeHead(200, {
      'Cache-Control': 'no-store',
      'Content-Length': body.length,
      'Content-Security-Policy': "default-src 'self'; connect-src 'self'; font-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'",
      'Content-Type': contentTypes.get(extname(candidate)) ?? 'application/octet-stream',
      'X-Content-Type-Options': 'nosniff',
      'X-Norn-Fixture': FIXTURE_VERSION,
    })
    response.end(body)
  } catch {
    sendJSON(response, 404, { error: 'fixture file not found' })
  }
}

export function createBrowserFixture({ distDir = DEFAULT_DIST } = {}) {
  let state = freshState()
  const server = createServer((request, response) => {
    const address = server.address()
    const expectedHost = address && typeof address === 'object' ? `${LOOPBACK}:${address.port}` : null
    if (!expectedHost || request.headers.host !== expectedHost) {
      return sendJSON(response, 421, { error: 'fixture accepts only its exact loopback authority' })
    }
    const expectedOrigin = `http://${expectedHost}`
    if (request.method === 'POST' && request.headers.origin && request.headers.origin !== expectedOrigin) {
      return sendJSON(response, 403, { error: 'fixture rejects cross-origin POST requests' })
    }

    const rawPath = (request.url ?? '/').split('?', 1)[0]
    try {
      if (decodeURIComponent(rawPath).split('/').includes('..')) return sendJSON(response, 400, { error: 'fixture rejects path traversal' })
    } catch {
      return sendJSON(response, 400, { error: 'invalid URL encoding' })
    }

    const url = new URL(request.url ?? '/', expectedOrigin)
    if (request.method === 'GET' && url.pathname === '/__fixture/state') return sendJSON(response, 200, state)
    if (request.method === 'POST' && url.pathname === '/__fixture/reset') {
      state = freshState()
      return sendJSON(response, 200, state)
    }
    if (url.pathname.startsWith('/api/')) {
      Promise.resolve(routeAPI(request, response, url.pathname, state)).catch((error) => {
        if (!response.headersSent) sendJSON(response, 400, { error: error instanceof Error ? error.message : String(error) })
      })
      return
    }
    if (request.method !== 'GET' && request.method !== 'HEAD') return sendJSON(response, 405, { error: 'method not allowed' })
    void serveStatic(response, url.pathname, resolve(distDir))
  })

  server.on('upgrade', (_request, socket) => socket.destroy())

  return {
    server,
    async listen(port = 0) {
      if (!Number.isInteger(port) || port < 0 || port > 65535) throw new Error('fixture port must be an integer from 0 through 65535')
      await new Promise((resolveListen, reject) => {
        server.once('error', reject)
        server.listen(port, LOOPBACK, () => {
          server.off('error', reject)
          resolveListen()
        })
      })
      const address = server.address()
      if (!address || typeof address === 'string' || address.address !== LOOPBACK) throw new Error('fixture failed to bind IPv4 loopback')
      return { host: LOOPBACK, port: address.port, url: `http://${LOOPBACK}:${address.port}` }
    },
    async close() {
      if (!server.listening) return
      await new Promise((resolveClose, reject) => server.close((error) => error ? reject(error) : resolveClose()))
    },
  }
}

function requestedPort(argv) {
  const index = argv.indexOf('--port')
  if (index === -1) return 0
  if (index === argv.length - 1 || !/^\d+$/.test(argv[index + 1])) throw new Error('--port requires a numeric value')
  return Number(argv[index + 1])
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const fixture = createBrowserFixture()
  const address = await fixture.listen(requestedPort(process.argv.slice(2)))
  process.stdout.write(`${JSON.stringify({ fixture: FIXTURE_VERSION, ...address })}\n`)
  const shutdown = async () => {
    await fixture.close()
    process.exit(0)
  }
  process.once('SIGINT', shutdown)
  process.once('SIGTERM', shutdown)
}
