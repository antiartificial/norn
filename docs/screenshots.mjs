#!/usr/bin/env node
/**
 * Capture the current Norn workspace UI with deterministic, non-production data.
 *
 * Start the UI first:
 *   cd v2/ui && pnpm dev --host 127.0.0.1
 *
 * Then run:
 *   node docs/screenshots.mjs
 *
 * Or point the capture at an already-running UI:
 *   NORN_UI_URL=http://127.0.0.1:18880 node docs/screenshots.mjs
 */
import { chromium } from 'playwright'
import { mkdirSync } from 'fs'
import { join } from 'path'
import {
  accessPatterns,
  activeIncidents,
  apps,
  cronHistory,
  deploySteps,
  deployments,
  events,
  functionExecutions,
  ingress,
  logs,
  operations,
  serviceManifest,
  stats,
} from './screenshot-fixtures.mjs'

const OUT = join(import.meta.dirname, 'public/screenshots')
mkdirSync(OUT, { recursive: true })

const BASE = process.env.NORN_UI_URL || 'http://localhost:5173'
const viewport = { width: 1360, height: 860 }

function json(body, status = 200) {
  return {
    status,
    contentType: 'application/json',
    body: JSON.stringify(body),
  }
}

function filteredDeployments(url) {
  const app = url.searchParams.get('app')
  const status = url.searchParams.get('status')
  return deployments.filter((deployment) => {
    if (app && deployment.app !== app) return false
    if (status && deployment.status !== status) return false
    return true
  })
}

async function installApiMocks(page) {
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname
    const method = route.request().method()

    if (path === '/api/apps' && method === 'GET') return route.fulfill(json(apps))
    if (path === '/api/stats') return route.fulfill(json(stats))
    if (path === '/api/health') {
      return route.fulfill(json({
        status: 'ok',
        services: { postgres: 'ok', nomad: 'ok', consul: 'ok', redpanda: 'ok', garage: 'ok' },
      }))
    }
    if (path === '/api/version') return route.fulfill(json({ version: 'v2.4.0' }))
    if (path === '/api/services/manifest') return route.fulfill(json(serviceManifest))
    if (path === '/api/access/patterns') return route.fulfill(json(accessPatterns))
    if (path === '/api/cloudflared/ingress') return route.fulfill(json(ingress))
    if (path === '/api/events/active') return route.fulfill(json(activeIncidents))
    if (path === '/api/events') return route.fulfill(json(events))
    if (path === '/api/operations/active') return route.fulfill(json(operations))
    if (path === '/api/operations') return route.fulfill(json(operations))
    if (path === '/api/deployments') return route.fulfill(json(filteredDeployments(url)))
    if (path.endsWith('/cron/history')) return route.fulfill(json(cronHistory))
    if (path.endsWith('/function/history')) return route.fulfill(json(functionExecutions))
    if (path.endsWith('/logs')) {
      return route.fulfill({ status: 200, contentType: 'text/plain', body: logs })
    }
    if (path.endsWith('/canary')) return route.fulfill(json(null))
    if (path.startsWith('/api/saga/')) return route.fulfill(json([]))
    if (method !== 'GET') return route.fulfill(json({ ok: true, id: 'invoke-docs-001' }))

    return route.fulfill(json({}))
  })
}

async function newPage(ctx) {
  const page = await ctx.newPage()
  page.on('pageerror', (error) => console.error(`  page error: ${error.message}`))
  await page.addInitScript(() => {
    localStorage.setItem('norn:tour-complete', '1')
    localStorage.setItem('norn-theme', 'dark')

    const sockets = []
    class MockWebSocket {
      static CONNECTING = 0
      static OPEN = 1
      static CLOSING = 2
      static CLOSED = 3

      constructor() {
        this.readyState = MockWebSocket.CONNECTING
        sockets.push(this)
        setTimeout(() => {
          this.readyState = MockWebSocket.OPEN
          this.onopen?.(new Event('open'))
        }, 30)
      }

      send() {}

      close() {
        this.readyState = MockWebSocket.CLOSED
        this.onclose?.(new CloseEvent('close'))
      }

      addEventListener(type, listener) {
        this[`on${type}`] = listener
      }

      removeEventListener(type) {
        this[`on${type}`] = null
      }
    }

    window.WebSocket = MockWebSocket
    window.__nornEmit = (event) => {
      for (const socket of sockets) socket.onmessage?.({ data: JSON.stringify(event) })
    }
  })
  await installApiMocks(page)
  return page
}

async function openRoute(page, path, readySelector) {
  await page.goto(`${BASE}${path}`, { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('nav a:has-text("Overview")', { timeout: 10_000 })
  try {
    await page.waitForSelector(readySelector, { timeout: 10_000 })
  } catch (error) {
    console.error(`  route ${path} did not render ${readySelector}: ${(await page.locator('body').innerText()).slice(0, 800)}`)
    throw error
  }
  await page.waitForTimeout(500)
}

async function emitDeployProgress(page) {
  for (const step of deploySteps) {
    await page.evaluate((payload) => {
      window.__nornEmit({
        type: 'deploy.step',
        appId: 'signal-sideband',
        payload: { step: payload.step, status: payload.status, sagaId: 'saga-812f4c19' },
      })
      window.__nornEmit({
        type: 'deploy.progress',
        appId: 'signal-sideband',
        payload: { step: payload.step, message: payload.message, node: 'mini' },
      })
    }, step)
    await page.waitForTimeout(80)
  }
}

async function capturePage(ctx, name, path, readySelector, prepare) {
  console.log(`  -> ${name}`)
  const page = await newPage(ctx)
  await openRoute(page, path, readySelector)
  if (prepare) await prepare(page)
  await page.screenshot({ path: join(OUT, name), fullPage: false })
  await page.close()
}

async function main() {
  const browser = await chromium.launch({ headless: true })
  const ctx = await browser.newContext({ viewport, deviceScaleFactor: 2 })

  await capturePage(ctx, 'dashboard.png', '/overview', '.overview-grid')

  console.log('  -> deploy-panel.png')
  const deployPage = await newPage(ctx)
  await openRoute(deployPage, '/apps/signal-sideband/overview', 'h2:has-text("signal-sideband")')
  await deployPage.getByRole('button', { name: 'Deploy', exact: true }).click()
  await emitDeployProgress(deployPage)
  await deployPage.waitForSelector('.deploy-panel .step-active')
  await deployPage.waitForTimeout(500)
  await deployPage.locator('.deploy-panel').screenshot({ path: join(OUT, 'deploy-panel.png') })
  await deployPage.close()

  await capturePage(ctx, 'operations-history.png', '/deploys', 'h4:has-text("Deploy History")', async (page) => {
    const earlier = page.locator('.history-earlier-toggle').first()
    if (await earlier.count()) await earlier.click()
    await page.waitForTimeout(250)
  })

  await capturePage(ctx, 'health-panel.png', '/apps/signal-sideband/overview', 'h2:has-text("signal-sideband")')
  await capturePage(ctx, 'log-viewer.png', '/apps/signal-sideband/logs', 'h3:has-text("signal-sideband")')
  await capturePage(ctx, 'cron-panel.png', '/apps/field-harbor-digest/cron', '.cron-entry')
  await capturePage(ctx, 'function-panel.png', '/apps/archive-thumb/functions', '.func-history', async (page) => {
    await page.locator('.func-textarea').fill('{"asset":"archive://renders/sideband.png","size":"poster"}')
  })

  await browser.close()
  console.log('\n  OK UI screenshots saved to docs/public/screenshots/')
}

main().catch(err => {
  console.error(err)
  process.exit(1)
})
