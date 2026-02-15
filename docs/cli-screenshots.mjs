#!/usr/bin/env node
/**
 * Capture CLI screenshots by rendering ANSI output in a styled HTML terminal via Playwright.
 * Usage: node docs/cli-screenshots.mjs
 */
import { chromium } from 'playwright'
import { mkdirSync, writeFileSync } from 'fs'
import { join } from 'path'
import { execSync } from 'child_process'

const OUT = join(import.meta.dirname, 'public/screenshots')
mkdirSync(OUT, { recursive: true })

const NORN = '/Users/0xadb/projects/norn/v2/bin/norn'

// Simple ANSI to HTML converter
function ansiToHtml(text) {
  const colorMap = {
    '30': '#4a4a4a', '31': '#ff6b6b', '32': '#69db7c', '33': '#ffd43b',
    '34': '#74c0fc', '35': '#da77f2', '36': '#66d9e8', '37': '#dee2e6',
    '90': '#868e96', '91': '#ff8787', '92': '#8ce99a', '93': '#ffe066',
    '94': '#91d5ff', '95': '#e599f7', '96': '#99e9f2', '97': '#f8f9fa',
  }

  let html = text
    // Escape HTML
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')

  // Replace ANSI codes with spans
  // Bold
  html = html.replace(/\x1b\[1m/g, '<span style="font-weight:bold">')
  // Dim
  html = html.replace(/\x1b\[2m/g, '<span style="opacity:0.6">')
  // Italic
  html = html.replace(/\x1b\[3m/g, '<span style="font-style:italic">')
  // Reset
  html = html.replace(/\x1b\[0m/g, '</span>')
  html = html.replace(/\x1b\[m/g, '</span>')

  // Colors
  for (const [code, color] of Object.entries(colorMap)) {
    const regex = new RegExp(`\\x1b\\[${code}m`, 'g')
    html = html.replace(regex, `<span style="color:${color}">`)
  }

  // Background colors
  const bgMap = {
    '40': '#4a4a4a', '41': '#ff6b6b', '42': '#69db7c', '43': '#ffd43b',
    '44': '#74c0fc', '45': '#da77f2', '46': '#66d9e8', '47': '#dee2e6',
  }
  for (const [code, color] of Object.entries(bgMap)) {
    const regex = new RegExp(`\\x1b\\[${code}m`, 'g')
    html = html.replace(regex, `<span style="background:${color};color:#1a1b26">`)
  }

  // Clean up any remaining ANSI escapes
  html = html.replace(/\x1b\[\d+(?:;\d+)*m/g, '')

  return html
}

function renderTerminal(title, content) {
  const htmlContent = ansiToHtml(content)
  return `<!DOCTYPE html>
<html>
<head>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    background: #0d1117;
    padding: 24px;
    font-family: 'SF Mono', 'Fira Code', 'JetBrains Mono', 'Cascadia Code', monospace;
  }
  .terminal {
    background: #161b22;
    border-radius: 10px;
    border: 1px solid #30363d;
    overflow: hidden;
    max-width: 720px;
  }
  .titlebar {
    background: #21262d;
    padding: 10px 16px;
    display: flex;
    align-items: center;
    gap: 8px;
    border-bottom: 1px solid #30363d;
  }
  .dot { width: 12px; height: 12px; border-radius: 50%; }
  .dot-red { background: #ff5f56; }
  .dot-yellow { background: #ffbd2e; }
  .dot-green { background: #27c93f; }
  .title {
    color: #8b949e;
    font-size: 13px;
    margin-left: 8px;
  }
  .content {
    padding: 16px 20px;
    color: #c9d1d9;
    font-size: 14px;
    line-height: 1.6;
    white-space: pre;
    overflow-x: auto;
  }
</style>
</head>
<body>
  <div class="terminal">
    <div class="titlebar">
      <div class="dot dot-red"></div>
      <div class="dot dot-yellow"></div>
      <div class="dot dot-green"></div>
      <span class="title">${title}</span>
    </div>
    <div class="content">${htmlContent}</div>
  </div>
</body>
</html>`
}

async function captureTerminal(browser, name, title, command) {
  console.log(`  → ${name}`)
  let output
  try {
    output = execSync(command, { encoding: 'utf-8', env: { ...process.env, TERM: 'xterm-256color', FORCE_COLOR: '1' }, timeout: 10000 })
  } catch (err) {
    output = err.stdout || err.message
  }

  const html = renderTerminal(title, output)
  const tmpPath = join(OUT, `_${name}.html`)
  writeFileSync(tmpPath, html)

  const page = await browser.newPage()
  await page.setViewportSize({ width: 800, height: 600 })
  await page.goto(`file://${tmpPath}`, { waitUntil: 'networkidle' })

  // Fit screenshot to content
  const terminal = page.locator('.terminal')
  const box = await terminal.boundingBox()
  await page.screenshot({
    path: join(OUT, name),
    clip: { x: 0, y: 0, width: box.x + box.width + 24, height: box.y + box.height + 24 },
  })
  await page.close()

  // Clean up tmp html
  const { unlinkSync } = await import('fs')
  unlinkSync(tmpPath)
}

async function main() {
  const browser = await chromium.launch({ headless: true })

  await captureTerminal(browser, 'cli-status.png', 'norn status', `${NORN} status`)
  await captureTerminal(browser, 'cli-version.png', 'norn version', `${NORN} version`)

  // Endpoints — requires API to be running
  await captureTerminal(browser, 'cli-endpoints.png', 'norn endpoints signal-sideband', `${NORN} endpoints signal-sideband`)

  await browser.close()
  console.log(`\n  ✔ CLI screenshots saved to docs/public/screenshots/`)
}

main().catch(err => {
  console.error(err)
  process.exit(1)
})
