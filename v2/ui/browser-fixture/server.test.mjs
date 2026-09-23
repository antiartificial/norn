import assert from 'node:assert/strict'
import { mkdtemp, mkdir, writeFile } from 'node:fs/promises'
import { request as httpRequest } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { createBrowserFixture } from './server.mjs'

async function withFixture(run) {
  const distDir = await mkdtemp(join(tmpdir(), 'norn-ui-fixture-'))
  await mkdir(join(distDir, 'assets'))
  await writeFile(join(distDir, 'index.html'), '<!doctype html><title>Norn fixture</title>')
  await writeFile(join(distDir, 'assets', 'fixture.js'), 'export {}')
  const fixture = createBrowserFixture({ distDir })
  const address = await fixture.listen()
  try {
    await run(address)
  } finally {
    await fixture.close()
  }
}

function post(url, path, key, body = { ref: 'HEAD' }) {
  return fetch(`${url}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': key },
    body: JSON.stringify(body),
  })
}

function rawRequest(url, path, headers = {}) {
  const authority = new URL(url)
  return new Promise((resolve, reject) => {
    const request = httpRequest({
      host: authority.hostname,
      port: authority.port,
      path,
      headers,
    }, (response) => {
      response.resume()
      response.once('end', () => resolve(response.statusCode))
    })
    request.once('error', reject)
    request.end()
  })
}

test('binds only loopback, serves the SPA, and fails closed on unknown APIs', async () => {
  await withFixture(async ({ host, port, url }) => {
    assert.equal(host, '127.0.0.1')
    const document = await fetch(`${url}/apps`)
    assert.equal(document.status, 200)
    assert.match(await document.text(), /Norn fixture/)

    const unknown = await fetch(`${url}/api/not-supported`)
    assert.equal(unknown.status, 404)
    const state = await fetch(`${url}/__fixture/state`).then((response) => response.json())
    assert.deepEqual(state.unexpectedApi, ['GET /api/not-supported'])

    assert.equal(await rawRequest(url, '/', { Host: `localhost:${port}` }), 421)
    assert.equal(await rawRequest(url, '/%2e%2e/%2e%2e/etc/passwd', { Host: `127.0.0.1:${port}` }), 400)
    const crossOrigin = await fetch(`${url}/__fixture/reset`, { method: 'POST', headers: { Origin: 'https://example.invalid' } })
    assert.equal(crossOrigin.status, 403)

    const apps = await fetch(`${url}/api/apps`).then((response) => response.json())
    assert.equal(apps[0].spec.deploy, true)
    const health = await fetch(`${url}/api/health`).then((response) => response.json())
    assert.deepEqual(health, { status: 'ok', services: { fixture: 'up' } })
    const groups = await fetch(`${url}/api/deploy-groups`).then((response) => response.json())
    assert.deepEqual(groups.groups[0], { name: 'core', apps: [{ app: 'api' }, { app: 'worker', waitReady: true }] })
  })
})

test('models lost response, auth rejection, and terminal replay with one key', async () => {
  await withFixture(async ({ url }) => {
    const key = 'fixture-preflight-key'
    await assert.rejects(post(url, '/api/apps/api/preflight', key))
    assert.equal((await post(url, '/api/apps/api/preflight', key)).status, 401)
    const replay = await post(url, '/api/apps/api/preflight', key)
    assert.equal(replay.status, 200)
    assert.deepEqual(await replay.json(), {
      sagaId: 'fixture-preflight-saga',
      operationId: 'fixture-preflight-operation',
      status: 'succeeded',
      replayed: true,
    })
    assert.equal((await post(url, '/api/apps/api/preflight', 'different-key')).status, 409)
    const state = await fetch(`${url}/__fixture/state`).then((response) => response.json())
    assert.deepEqual(state.preflight.keys, [key, key, key, 'different-key'])
    assert.equal(state.preflight.keyMismatch, true)
  })
})

test('models partial group recovery and rejects a changed parent key', async () => {
  await withFixture(async ({ url }) => {
    const first = await post(url, '/api/deploy-groups/core/deploy', 'fixture-group-key')
    assert.equal(first.status, 202)
    assert.equal((await first.json()).deploys[1].error, 'fixture worker unavailable')

    const replay = await post(url, '/api/deploy-groups/core/deploy', 'fixture-group-key')
    assert.equal(replay.status, 200)
    const replayBody = await replay.json()
    assert.equal(replayBody.replayed, true)
    assert.equal(replayBody.deploys[0].operationId, 'fixture-group-api-operation')
    assert.equal(replayBody.deploys[1].operationId, 'fixture-group-worker-operation')

    const changed = await post(url, '/api/deploy-groups/core/deploy', 'different-key')
    assert.equal(changed.status, 409)
    const state = await fetch(`${url}/__fixture/state`).then((response) => response.json())
    assert.equal(state.deployGroup.keyMismatch, true)
    assert.deepEqual(state.deployGroup.keys, ['fixture-group-key', 'fixture-group-key', 'different-key'])
  })
})

test('models a transient durable-operation poll before terminal success', async () => {
  await withFixture(async ({ url }) => {
    const accepted = await post(url, '/api/apps/api/deploy', 'fixture-deploy-key')
    assert.equal(accepted.status, 202)
    assert.equal((await accepted.json()).status, 'running')
    assert.equal((await fetch(`${url}/api/v1/operations/fixture-poll-operation`)).status, 503)
    const terminal = await fetch(`${url}/api/v1/operations/fixture-poll-operation`)
    assert.equal(terminal.status, 200)
    assert.equal((await terminal.json()).status, 'succeeded')
  })
})
