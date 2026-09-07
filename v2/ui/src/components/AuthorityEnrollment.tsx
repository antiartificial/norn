import { useState } from 'react'
import { ApiError, apiFetch, clearMemoryAccessToken, setMemoryAccessToken } from '../lib/api.ts'
import { Button, StatusChip } from './ui/index.ts'

interface EnrollmentStart {
  id: string
  userCode: string
  verifier: string
  expiresAt: string
  pollPath: string
}

interface IssuedToken {
  token: string
  tokenId: string
  deviceId: string
  scopes: string[]
  expiresAt: string
}

function base64URL(bytes: ArrayBuffer): string {
  let binary = ''
  for (const byte of new Uint8Array(bytes)) binary += String.fromCharCode(byte)
  return btoa(binary).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '')
}

function pairingTransportAllowed(): boolean {
  const hostname = window.location.hostname
  return window.location.protocol === 'https:' || hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '[::1]'
}

export function AuthorityEnrollment({ onAuthenticated, onSignedOut }: { onAuthenticated: () => void; onSignedOut: () => void }) {
  const [enrollment, setEnrollment] = useState<EnrollmentStart>()
  const [busy, setBusy] = useState(false)
  const [session, setSession] = useState<IssuedToken>()
  const [message, setMessage] = useState<string>()
  const [requestWrite, setRequestWrite] = useState(false)
  const canPair = pairingTransportAllowed() && Boolean(globalThis.crypto?.subtle)

  const begin = async () => {
    if (!canPair || busy) return
    setBusy(true)
    setMessage(undefined)
    try {
      const keys = await crypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign', 'verify'])
      const publicKey = await crypto.subtle.exportKey('raw', keys.publicKey)
      const started = await apiFetch<EnrollmentStart>('/api/v1/enrollments', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          deviceName: 'NornUI browser', platform: 'web', model: navigator.userAgent.slice(0, 120), appVersion: 'norn-ui',
          publicKey: base64URL(publicKey), requestedScopes: requestWrite ? ['api:read', 'api:write'] : ['api:read'],
        }),
      })
      setEnrollment(started)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : 'Pairing could not be started.')
    } finally {
      setBusy(false)
    }
  }

  const checkApproval = async () => {
    if (!enrollment || busy) return
    setBusy(true)
    setMessage(undefined)
    try {
      const issued = await apiFetch<IssuedToken>(enrollment.pollPath, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ verifier: enrollment.verifier }),
      })
      setMemoryAccessToken(issued.token)
      setSession(issued)
      setEnrollment(undefined)
      onAuthenticated()
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) {
        setMessage('Approval is still pending or the pairing request has expired. Ask an administrator to approve the displayed code, then check again.')
      } else {
        setMessage(error instanceof Error ? error.message : 'Approval could not be checked.')
      }
    } finally {
      setBusy(false)
    }
  }

  const signOut = async () => {
    setBusy(true)
    try {
      await apiFetch('/api/v1/auth/revoke', { method: 'POST' })
    } catch {
      // Clearing the in-memory credential is still required if the network is gone.
    } finally {
      clearMemoryAccessToken()
      setSession(undefined)
      setEnrollment(undefined)
      setMessage('Signed out. This browser no longer retains a control-plane credential.')
      onSignedOut()
      setBusy(false)
    }
  }

  if (session) {
    return <section className="panel" aria-labelledby="browser-pairing-title">
      <div className="section-heading"><div><span className="eyebrow">Browser session</span><h2 id="browser-pairing-title">Scoped browser pairing</h2></div><StatusChip tone="success" label={session.scopes.join(', ')} /></div>
      <p className="panel-intro">This session is held only in memory and will be lost on reload. It is not saved to browser storage, a URL, or logs.</p>
      <Button variant="ghost" size="sm" type="button" disabled={busy} onClick={() => void signOut()}>Sign out and revoke</Button>
      {message && <p role="status" className="fleet-change-note">{message}</p>}
    </section>
  }

  return <section className="panel" aria-labelledby="browser-pairing-title">
    <div className="section-heading"><div><span className="eyebrow">Browser access</span><h2 id="browser-pairing-title">Pair this browser</h2></div><StatusChip tone="neutral" label="approval required" /></div>
    <p className="panel-intro">Fleet records require an enrolled device token. A native Norn client remains the primary path; a paired browser session is held only in memory and will be lost on reload. Browser pairing requests only <code>api:read</code> by default. An administrator can approve the code and may grant <code>api:write</code> only when it was explicitly requested by a trusted client.</p>
    {!canPair && <p role="alert" className="fleet-change-warning">Browser pairing requires HTTPS (or loopback) and WebCrypto P-256 support. Use the native Norn client if this page is not served over a trusted transport.</p>}
    {!enrollment ? <><label><input type="checkbox" checked={requestWrite} onChange={(event) => setRequestWrite(event.target.checked)} /> Request operator permission (<code>api:write</code>)</label><p className="fleet-change-note">Operators may approve fewer requested permissions; browser pairing can never request administrator access.</p><Button variant="primary" type="button" disabled={!canPair || busy} onClick={() => void begin()}>{busy ? 'Starting pairing…' : 'Pair this browser'}</Button></> : <div className="fleet-change-note" role="status"><p>Give this one-time code to an administrator:</p><p><code>{enrollment.userCode}</code></p><p>It expires at {new Date(enrollment.expiresAt).toLocaleTimeString()}. This page does not poll; check after approval.</p><Button variant="primary" type="button" disabled={busy} onClick={() => void checkApproval()}>{busy ? 'Checking…' : 'Check approval'}</Button></div>}
    {message && <p role="status" className="fleet-change-warning">{message}</p>}
  </section>
}
