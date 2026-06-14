#!/usr/bin/env node
/**
 * Capture Norn UI screenshots using Playwright.
 *
 * Start the UI first:
 *   cd ui && pnpm dev --host 127.0.0.1
 *
 * Then run:
 *   node docs/screenshots.mjs
 */
import { chromium } from 'playwright'
import { mkdirSync } from 'fs'
import { join } from 'path'
import {
  apps,
  cronExecutions,
  deploySteps,
  deployments,
  functionExecutions,
  healthChecks,
  logs,
  stats,
} from './screenshot-fixtures.mjs'

const OUT = join(import.meta.dirname, 'public/screenshots')
mkdirSync(OUT, { recursive: true })

const BASE = process.env.NORN_UI_URL || 'http://localhost:5173'
const viewport = { width: 1360, height: 860 }

function json(body) {
  return {
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify(body),
  }
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
        services: [
          { name: 'postgres', status: 'ok' },
          { name: 'nomad', status: 'ok' },
          { name: 'consul', status: 'ok' },
          { name: 'redpanda', status: 'ok' },
          { name: 'sops', status: 'ok' },
        ],
      }))
    }
    if (path === '/api/version') return route.fulfill(json({ version: 'v2.0.0-continuity' }))
    if (path === '/api/deployments') return route.fulfill(json({ deployments, total: deployments.length }))
    if (path.endsWith('/health-checks')) {
      const app = path.split('/')[3]
      return route.fulfill(json({ checks: healthChecks[app] ?? [] }))
    }
    if (path.endsWith('/cron/history')) return route.fulfill(json({ executions: cronExecutions }))
    if (path.endsWith('/function/history')) return route.fulfill(json({ executions: functionExecutions }))
    if (path.endsWith('/logs')) {
      return route.fulfill({ status: 200, contentType: 'text/plain', body: logs })
    }
    if (method !== 'GET') return route.fulfill(json({ ok: true }))

    return route.fulfill(json({}))
  })
}

async function newPage(ctx) {
  const page = await ctx.newPage()
  await page.addInitScript(() => {
    localStorage.setItem('norn:tour-complete', '1')

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
      for (const socket of sockets) {
        socket.onmessage?.({ data: JSON.stringify(event) })
      }
    }
  })
  await installApiMocks(page)
  return page
}

async function openApp(page) {
  await page.goto(BASE, { waitUntil: 'networkidle' })
  await page.waitForSelector('.app-card', { timeout: 10_000 })
  await page.waitForTimeout(500)
}

async function emitDeployProgress(page) {
  for (const step of deploySteps) {
    await page.evaluate((payload) => {
      window.__nornEmit({
        type: 'deploy.step',
        appId: 'signal-sideband',
        payload,
      })
    }, step)
    await page.waitForTimeout(80)
  }
}

async function main() {
  const browser = await chromium.launch({ headless: true })
  const ctx = await browser.newContext({ viewport, deviceScaleFactor: 2 })

  console.log('  -> dashboard.png')
  const dashboard = await newPage(ctx)
  await openApp(dashboard)
  await dashboard.screenshot({ path: join(OUT, 'dashboard.png'), fullPage: false })
  await dashboard.close()

  console.log('  -> deploy-panel.png')
  const deployPage = await newPage(ctx)
  await openApp(deployPage)
  await deployPage.locator('.app-card', { hasText: 'signal-sideband' }).locator('button:has-text("Deploy Latest")').click()
  await emitDeployProgress(deployPage)
  await deployPage.waitForTimeout(1200)
  await deployPage.locator('.deploy-panel').screenshot({ path: join(OUT, 'deploy-panel.png') })
  await deployPage.close()

  console.log('  -> operations-history.png')
  const historyPage = await newPage(ctx)
  await openApp(historyPage)
  await historyPage.locator('header button:has-text("History")').click()
  await historyPage.waitForSelector('.history-panel')
  await historyPage.locator('.history-row', { hasText: 'signal-sideband' }).nth(1).click()
  await historyPage.waitForTimeout(300)
  await historyPage.screenshot({ path: join(OUT, 'operations-history.png'), fullPage: false })
  await historyPage.close()

  console.log('  -> health-panel.png')
  const healthPage = await newPage(ctx)
  await openApp(healthPage)
  await healthPage.locator('.app-card', { hasText: 'signal-sideband' }).locator('.sparkline-strip').click()
  await healthPage.waitForSelector('.health-panel')
  await healthPage.screenshot({ path: join(OUT, 'health-panel.png'), fullPage: false })
  await healthPage.close()

  console.log('  -> log-viewer.png')
  const logsPage = await newPage(ctx)
  await openApp(logsPage)
  await logsPage.locator('.app-card', { hasText: 'signal-sideband' }).locator('button:has-text("Logs")').click()
  await logsPage.waitForSelector('.log-viewer')
  await logsPage.waitForTimeout(500)
  await logsPage.screenshot({ path: join(OUT, 'log-viewer.png'), fullPage: false })
  await logsPage.close()

  console.log('  -> cron-panel.png')
  const cronPage = await newPage(ctx)
  await openApp(cronPage)
  await cronPage.locator('.app-card', { hasText: 'field-harbor-digest' }).locator('button:has-text("History")').click()
  await cronPage.waitForSelector('.cron-panel')
  await cronPage.locator('.cron-execution-row').first().click()
  await cronPage.waitForTimeout(300)
  await cronPage.screenshot({ path: join(OUT, 'cron-panel.png'), fullPage: false })
  await cronPage.close()

  console.log('  -> function-panel.png')
  const funcPage = await newPage(ctx)
  await openApp(funcPage)
  await funcPage.locator('.app-card', { hasText: 'archive-thumb' }).locator('button:has-text("History")').click()
  await funcPage.waitForSelector('.cron-panel')
  await funcPage.locator('textarea').fill('{"asset":"r2://archive-renders/sideband.png","size":"poster"}')
  await funcPage.locator('.cron-execution-row').first().click()
  await funcPage.waitForTimeout(300)
  await funcPage.screenshot({ path: join(OUT, 'function-panel.png'), fullPage: false })
  await funcPage.close()

  await browser.close()
  console.log('\n  OK UI screenshots saved to docs/public/screenshots/')
}

main().catch(err => {
  console.error(err)
  process.exit(1)
})
