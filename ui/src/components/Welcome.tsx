import { useState } from 'react'

interface Props {
  onDismiss: () => void
}

const steps = [
  {
    title: 'Welcome to Norn',
    icon: '&#9776;',
    body: `Norn is your personal control plane. It discovers apps in your
projects directory by looking for <code>infraspec.yaml</code> files,
shows their health, and lets you deploy, restart, and roll back with one click.`,
  },
  {
    title: 'Add your first app',
    icon: '&#10010;',
    body: `Create an <code>infraspec.yaml</code> in any project directory:

<pre>app: my-app
role: webserver
port: 3000
healthcheck: /health
build:
  dockerfile: Dockerfile
  test: npm test
artifacts:
  retain: 5</pre>

Norn will pick it up automatically.`,
  },
  {
    title: 'Secrets & Config',
    icon: '&#128274;',
    body: `Secrets are encrypted at rest with <strong>SOPS + age</strong>.
Each app can have a <code>secrets.enc.yaml</code> that Norn decrypts
and syncs to Kubernetes Secrets at deploy time. Secret <em>names</em> are
visible in the UI, but values never leave the server.`,
  },
  {
    title: 'Deploy Pipeline',
    icon: '&#9654;',
    body: `When you deploy, Norn runs a pipeline:
<ol>
  <li><strong>Build</strong> — docker build with your Dockerfile</li>
  <li><strong>Test</strong> — runs your test command in the container</li>
  <li><strong>Snapshot</strong> — pg_dump before any migration</li>
  <li><strong>Migrate</strong> — runs schema migrations</li>
  <li><strong>Deploy</strong> — updates the K8s deployment</li>
</ol>
Every step is logged. If any step fails, nothing after it runs.`,
  },
  {
    title: 'You\'re ready',
    icon: '&#10004;',
    body: `<strong>Useful commands:</strong>
<pre>make dev       # start API + UI
make doctor    # check all services
make infra     # start Valkey + Redpanda
make build     # production build</pre>

This tour is always available from the <strong>?</strong> button in the header.`,
  },
]

export function Welcome({ onDismiss }: Props) {
  const [step, setStep] = useState(0)
  const current = steps[step]
  const isLast = step === steps.length - 1

  return (
    <div className="welcome-overlay">
      <div className="welcome-modal">
        <div className="welcome-step-indicator">
          {steps.map((_, i) => (
            <span key={i} className={`welcome-dot ${i === step ? 'active' : ''} ${i < step ? 'done' : ''}`} />
          ))}
        </div>

        <div className="welcome-icon" dangerouslySetInnerHTML={{ __html: current.icon }} />
        <h2>{current.title}</h2>
        <div className="welcome-body" dangerouslySetInnerHTML={{ __html: current.body }} />

        <div className="welcome-actions">
          {step > 0 && (
            <button className="btn" onClick={() => setStep(step - 1)}>
              Back
            </button>
          )}
          <div className="welcome-spacer" />
          {!isLast ? (
            <button className="btn btn-primary" onClick={() => setStep(step + 1)}>
              Next
            </button>
          ) : (
            <button className="btn btn-primary" onClick={onDismiss}>
              Get started
            </button>
          )}
        </div>

        <button className="welcome-skip" onClick={onDismiss}>
          skip tour
        </button>
      </div>
    </div>
  )
}
