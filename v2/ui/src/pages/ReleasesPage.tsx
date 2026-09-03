import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '../lib/api.ts'
import { clearDurableIntent, durableIntent, type DurableIntent } from '../lib/durableIntent.ts'
import { useRuntimeContext } from '../runtime/AppRuntime.tsx'
import type { Deployment, ReleaseQualification, ReleaseQualificationResponse } from '../types/index.ts'
import { Button, CopyButton, EmptyState, ErrorState, StatusChip, useToast } from '../components/ui/index.ts'

function short(value: string, count = 12): string {
  return value.length > count ? `${value.slice(0, count)}…` : value
}

function isDeploymentID(value: string): boolean { return /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(value.trim()) }
function isExpired(qualification: ReleaseQualification): boolean { return Boolean(qualification.expiresAt && new Date(qualification.expiresAt).getTime() < Date.now()) }

function qualificationTone(qualification: ReleaseQualification): 'success' | 'warning' | 'danger' | 'neutral' {
  if (isExpired(qualification)) return 'warning'
  return 'success'
}

function attestationMode(qualification: ReleaseQualification): string {
  return qualification.candidate.attestation.mode ?? 'not reported'
}

function verifierIdentity(qualification: ReleaseQualification): string {
  return qualification.candidate.attestation.verifier ?? qualification.candidate.attestation.verifierIdentity ?? 'not reported'
}

function signedQualification(value: unknown): value is ReleaseQualification {
  if (!value || typeof value !== 'object') return false
  const receipt = value as Record<string, unknown>
	if (!['id', 'app', 'environment', 'deploymentId', 'sourceSha', 'artifact', 'issuedAt', 'expiresAt', 'keyId', 'signature'].every((key) => typeof receipt[key] === 'string' && String(receipt[key]).trim() !== '')) return false
	const dsse = receipt.dsse as Record<string, unknown> | undefined
	const candidate = receipt.candidate as Record<string, unknown> | undefined
	const signature = Array.isArray(dsse?.signatures) && dsse.signatures.length === 1 ? dsse.signatures[0] as Record<string, unknown> : undefined
	return receipt.schemaVersion === 'norn.release-qualification/v2' && Boolean(
		dsse && dsse.payloadType === 'application/vnd.norn.release-qualification.v2+json' && typeof dsse.payload === 'string' && signature && signature.keyid === receipt.keyId && signature.sig === receipt.signature &&
		candidate && ['provider', 'repository', 'repositoryId', 'ownerId', 'runId', 'workflowRef', 'workflowSha', 'signerWorkflowRef', 'signerWorkflowSha', 'ref'].every((key) => typeof candidate[key] === 'string' && String(candidate[key]).trim() !== '') &&
		candidate.attestation && typeof candidate.attestation === 'object'
	)
}

function EnvironmentBadge({ environment }: { environment: string }) {
  const tone = environment === 'production' ? 'danger' : environment === 'staging' ? 'warning' : 'neutral'
  return <StatusChip tone={tone} label={environment} />
}

export function ReleasesPage() {
  const { apps, environment, releasePipelineAvailable } = useRuntimeContext()
  const [appId, setAppId] = useState(apps[0]?.spec.name ?? '')
  const [deploymentId, setDeploymentId] = useState('')
  const [deploymentIdError, setDeploymentIdError] = useState('')
  const queryClient = useQueryClient()
  const { toast } = useToast()
  const isProduction = environment.id === 'production'
  const isStaging = environment.id === 'staging'

  useEffect(() => {
    if (!appId && apps[0]) setAppId(apps[0].spec.name)
  }, [appId, apps])

  const selectApp = (value: string) => {
    setAppId(value)
  }

  const qualifications = useQuery({
    queryKey: ['release-qualifications', appId],
    queryFn: () => apiFetch<ReleaseQualificationResponse>(`/api/v1/apps/${encodeURIComponent(appId ?? '')}/qualifications`),
    enabled: Boolean(appId) && releasePipelineAvailable,
    staleTime: 10_000,
  })
  const deployments = useQuery({
    queryKey: ['deployments', { app: appId, limit: 12 }],
    queryFn: () => apiFetch<Deployment[]>(`/api/deployments?app=${encodeURIComponent(appId ?? '')}&limit=12`),
    enabled: isStaging && Boolean(appId) && releasePipelineAvailable,
    staleTime: 10_000,
  })

  const qualificationRequest = useMutation({
    mutationFn: async ({ deploymentId: value, intent }: { deploymentId: string; intent: DurableIntent }) => apiFetch<ReleaseQualification>(`/api/v1/apps/${encodeURIComponent(appId)}/qualifications`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Idempotency-Key': intent.key },
      body: JSON.stringify({ deploymentId: value }),
    }),
    onSuccess: (qualification, variables) => {
      clearDurableIntent(variables.intent)
      queryClient.setQueryData<ReleaseQualificationResponse>(['release-qualifications', appId], (current) => ({
		...(current ?? { schemaVersion: 'norn.release-qualifications/v2', qualifications: [], count: 0 }),
        qualifications: [qualification, ...(current?.qualifications ?? []).filter((item) => item.id !== qualification.id)],
        count: Math.max(current?.count ?? 0, (current?.qualifications ?? []).length + 1),
      }))
      queryClient.invalidateQueries({ queryKey: ['release-qualifications', appId] })
      toast({ kind: 'success', title: 'Staging qualification recorded', description: `${short(qualification.deploymentId)} · signed evidence is ready to copy.` })
    },
    onError: (error) => toast({ kind: 'error', title: 'Qualification was not recorded', description: error instanceof Error ? error.message : appId }),
  })

	const qualificationItems = (qualifications.data?.qualifications ?? []).filter(signedQualification)

  if (!releasePipelineAvailable) {
    return <EmptyState icon="fa-shield-halved" title="Release pipeline unavailable" hint="This server does not advertise the release provenance, qualification, and promotion capabilities." />
  }

  const qualify = (value: string) => qualificationRequest.mutate({ deploymentId: value, intent: durableIntent(`release:${appId}:qualification`, { deploymentId: value }) })

  const submitQualification = () => {
    const value = deploymentId.trim()
    if (!isDeploymentID(value)) {
      setDeploymentIdError('Enter the UUID from a successful staging deployment.')
      return
    }
    setDeploymentIdError('')
    qualify(value)
  }

  return (
    <section className="release-desk" aria-labelledby="release-desk-title">
      <header className="release-desk-header">
        <div>
          <div className="release-kicker">Build once · promote unchanged</div>
          <h2 id="release-desk-title">Release pipeline</h2>
          <p>Every action records an immutable operation receipt. Source SHA and OCI digest are never inferred from a branch.</p>
        </div>
        <EnvironmentBadge environment={environment.id} />
      </header>

      <div className="release-identity" role="status">
        <span>Connected environment</span><strong>{environment.id}</strong>
        <span>Policy profile</span><strong>{environment.profile}</strong>
      </div>

      {isStaging && <section className="release-card" aria-labelledby="stage-title"><div className="release-card-heading"><div><h3 id="stage-title">CI-owned staging lane</h3><p>The protected CI workflow is the only path that can submit a source, immutable artifact, and verified candidate. Monitor its queued release here, then record its qualification after it succeeds.</p></div><StatusChip tone="warning" label="CI only" /></div></section>}

      {isStaging && (
        <section className="release-card" aria-labelledby="record-qualification-title">
          <div className="release-card-heading"><div><h3 id="record-qualification-title">Record successful staging qualification</h3><p>Select a successful staging deployment or enter its exact UUID. This creates the first signed evidence receipt.</p></div><StatusChip tone="warning" label="staging only" /></div>
          <div className="release-qualification-entry">
            <label>Successful staging deployment<select value="" onChange={(event) => { if (event.target.value) { setDeploymentId(event.target.value); setDeploymentIdError('') } }} aria-describedby="deployment-id-help"><option value="">Choose a recent successful deployment</option>{(deployments.data ?? []).filter((deployment) => ['healthy', 'deployed', 'succeeded'].includes(deployment.status.toLowerCase())).map((deployment) => <option key={deployment.id} value={deployment.id}>{short(deployment.id, 24)} · {short(deployment.commitSha)} · {deployment.status}</option>)}</select></label>
            <label>Deployment UUID<input value={deploymentId} onChange={(event) => { setDeploymentId(event.target.value); setDeploymentIdError('') }} spellCheck={false} autoCapitalize="off" autoComplete="off" placeholder="xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" aria-describedby="deployment-id-help deployment-id-error" aria-invalid={Boolean(deploymentIdError)} /></label>
          </div>
          <p id="deployment-id-help" className="release-field-hint">Use the deployment ID shown when the staging release operation succeeds. Only a successful staging deployment can be qualified.</p>
          {deploymentIdError && <p id="deployment-id-error" className="release-field-error" role="alert">{deploymentIdError}</p>}
          <div className="release-actions"><Button variant="secondary" icon="fa-certificate" loading={qualificationRequest.isPending} disabled={!appId || qualificationRequest.isPending} onClick={submitQualification}>Record qualification</Button>{deployments.isLoading && <span className="release-field-hint" role="status">Loading recent deployments…</span>}</div>
        </section>
      )}

      {isProduction && (
        <section className="release-card release-production-card" aria-labelledby="production-title">
          <div className="release-card-heading"><div><h3 id="production-title">GitHub-governed production lane</h3><p>This page is read-only for production promotion. Norn accepts production releases only from the protected GitHub release workflow.</p></div><StatusChip tone="danger" label="GitHub approval required" /></div>
          <label className="release-app-picker">Application<select value={appId} onChange={(event) => selectApp(event.target.value)}><option value="" disabled>Select an application</option>{apps.map((app) => <option key={app.spec.name} value={app.spec.name}>{app.spec.name}</option>)}</select></label>
          <p className="release-warning"><i className="fawsb fa-shield-halved" aria-hidden /> To promote, create the protected <code>v*</code> tag at the qualified commit. GitHub Environment approval runs the reusable production workflow, which sends the signed staging receipt to Norn.</p>
          <p className="release-field-hint">Use the evidence below to review the source SHA, digest, and signer. Follow the GitHub Actions run for approval and promotion status; operators cannot paste evidence or queue production work from this UI.</p>
        </section>
      )}

      <section className="release-card" aria-labelledby="qualification-title">
        <div className="release-card-heading"><div><h3 id="qualification-title">Staging qualifications</h3><p>Qualification binds deployment evidence to its exact source and artifact.</p></div><Button size="sm" variant="ghost" icon="fa-rotate" onClick={() => qualifications.refetch()}>Refresh</Button></div>
        {qualifications.isError && <ErrorState message={qualifications.error instanceof Error ? qualifications.error.message : 'Could not load qualifications'} />}
        {qualifications.isLoading && <p className="release-field-hint" role="status">Loading qualification evidence…</p>}
        {!qualifications.isLoading && !qualifications.isError && qualificationItems.length === 0 && <EmptyState icon="fa-clipboard-check" title="No qualification evidence" hint="Deploy a release to staging, complete its checks, then record its qualification." />}
        {qualificationItems.map((qualification) => (
          <article className="qualification-row" key={qualification.id}>
            <div className="qualification-main">
              <div className="qualification-title"><StatusChip tone={qualificationTone(qualification)} label={isExpired(qualification) ? 'expired' : 'signed staging evidence'} /><strong>{short(qualification.deploymentId)}</strong><CopyButton value={qualification.deploymentId} label="Copy deployment ID" /></div>
	              <dl className="qualification-evidence"><div><dt>Source SHA</dt><dd><code title={qualification.sourceSha}>{short(qualification.sourceSha)}</code><CopyButton value={qualification.sourceSha} label="Copy source SHA" /></dd></div><div><dt>Artifact digest</dt><dd><code title={qualification.artifact}>{short(qualification.artifact, 28)}</code><CopyButton value={qualification.artifact} label="Copy artifact digest" /></dd></div><div><dt>Attestation mode</dt><dd><code>{attestationMode(qualification)}</code></dd></div><div><dt>Verifier</dt><dd><code>{verifierIdentity(qualification)}</code></dd></div><div><dt>Signer</dt><dd><code>{qualification.dsse.signatures[0]?.keyid}</code></dd></div>{qualification.expiresAt && <div><dt>Evidence expires</dt><dd><time dateTime={qualification.expiresAt}>{new Date(qualification.expiresAt).toLocaleString()}</time></dd></div>}</dl>
              <CopyButton value={JSON.stringify(qualification)} label="Copy complete signed qualification JSON" />
            </div>
            <div className="qualification-actions">{isStaging && <Button size="sm" variant="secondary" disabled={qualificationRequest.isPending} onClick={() => qualify(qualification.deploymentId)}>Re-qualify</Button>}</div>
          </article>
        ))}
      </section>
    </section>
  )
}
