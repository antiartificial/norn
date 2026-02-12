#!/usr/bin/env node
/**
 * Capture Norn UI screenshots using Playwright.
 * Usage: node docs/screenshots.mjs
 */
import { chromium } from 'playwright'
import { mkdirSync } from 'fs'
import { join } from 'path'

const OUT = join(import.meta.dirname, 'public/screenshots')
mkdirSync(OUT, { recursive: true })

const BASE = 'http://localhost:5173'
const viewport = { width: 1280, height: 800 }

async function newPage(ctx) {
  const page = await ctx.newPage()
  // Dismiss the welcome tour overlay
  await page.addInitScript(() => {
    localStorage.setItem('norn:tour-complete', '1')
  })
  return page
}

async function main() {
  const browser = await chromium.launch({ headless: true })
  const ctx = await browser.newContext({ viewport, deviceScaleFactor: 2 })

  // 1. Dashboard — main app grid
  console.log('  → dashboard.png')
  const dashboard = await newPage(ctx)
  await dashboard.goto(BASE, { waitUntil: 'networkidle' })
  await dashboard.waitForTimeout(1500)
  await dashboard.screenshot({ path: join(OUT, 'dashboard.png'), fullPage: false })
  await dashboard.close()

  // 2. Deploy panel — click deploy on first app card
  console.log('  → deploy-panel.png')
  const deployPage = await newPage(ctx)
  await deployPage.goto(BASE, { waitUntil: 'networkidle' })
  await deployPage.waitForTimeout(1000)
  const deployBtn = deployPage.locator('button:has-text("Deploy")').first()
  if (await deployBtn.isVisible({ timeout: 3000 }).catch(() => false)) {
    await deployBtn.click()
    await deployPage.waitForTimeout(800)
  }
  await deployPage.screenshot({ path: join(OUT, 'deploy-panel.png'), fullPage: false })
  await deployPage.close()

  // 3. Health panel — click the health/services button
  console.log('  → health-panel.png')
  const healthPage = await newPage(ctx)
  await healthPage.goto(BASE, { waitUntil: 'networkidle' })
  await healthPage.waitForTimeout(1000)
  // Try clicking the health status bar or services button
  const healthBtn = healthPage.locator('button:has-text("Health"), button:has-text("Services"), [data-panel="health"]').first()
  if (await healthBtn.isVisible({ timeout: 3000 }).catch(() => false)) {
    await healthBtn.click()
    await healthPage.waitForTimeout(800)
  }
  await healthPage.screenshot({ path: join(OUT, 'health-panel.png'), fullPage: false })
  await healthPage.close()

  // 4. Cron panel — look for a cron app's History button
  console.log('  → cron-panel.png')
  const cronPage = await newPage(ctx)
  await cronPage.goto(BASE, { waitUntil: 'networkidle' })
  await cronPage.waitForTimeout(1000)
  const cronBtn = cronPage.locator('button:has-text("History")').first()
  if (await cronBtn.isVisible({ timeout: 3000 }).catch(() => false)) {
    await cronBtn.click()
    await cronPage.waitForTimeout(800)
  }
  await cronPage.screenshot({ path: join(OUT, 'cron-panel.png'), fullPage: false })
  await cronPage.close()

  // 5. Log viewer — click Logs on first app
  console.log('  → log-viewer.png')
  const logsPage = await newPage(ctx)
  await logsPage.goto(BASE, { waitUntil: 'networkidle' })
  await logsPage.waitForTimeout(1000)
  const logsBtn = logsPage.locator('button:has-text("Logs")').first()
  if (await logsBtn.isVisible({ timeout: 3000 }).catch(() => false)) {
    await logsBtn.click()
    await logsPage.waitForTimeout(2000) // let logs stream in
  }
  await logsPage.screenshot({ path: join(OUT, 'log-viewer.png'), fullPage: false })
  await logsPage.close()

  // 6. Function panel — look for function invoke button
  console.log('  → function-panel.png')
  const funcPage = await newPage(ctx)
  await funcPage.goto(BASE, { waitUntil: 'networkidle' })
  await funcPage.waitForTimeout(1000)
  const funcBtn = funcPage.locator('button:has-text("Invoke")').first()
  if (await funcBtn.isVisible({ timeout: 3000 }).catch(() => false)) {
    await funcBtn.click()
    await funcPage.waitForTimeout(800)
  }
  await funcPage.screenshot({ path: join(OUT, 'function-panel.png'), fullPage: false })
  await funcPage.close()

  await browser.close()
  console.log(`\n  ✔ Screenshots saved to docs/public/screenshots/`)
}

main().catch(err => {
  console.error(err)
  process.exit(1)
})
