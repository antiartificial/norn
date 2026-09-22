import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import {
  Background, Controls, MiniMap, Panel, ReactFlow, useNodesState,
  type Edge, type NodeMouseHandler,
} from '@xyflow/react'
import { fleetNodeTypes } from '../components/fleet/FleetNode.tsx'
import {
  layout, regionFrameNodes, EDGE_STYLE, EDGE_ORDER, EDGE_LABEL,
  type FleetFlowNode, type FleetRegionNode, type EdgeKind,
} from '../lib/fleetLayout.ts'
import {
  defaultDraft, validateDraft, costLines, totalMonthlyUSD, clusterYaml, fleetDocuments, totalApps,
  type FleetDraft, type FleetDBMode, type FleetDBEngine, type FleetEdgeMode,
  type FleetReplicaRegion, type FleetServiceKind, type FleetSeverity,
} from '../lib/fleetDraft.ts'
import { NODE_SIZES, MANAGED_SIZES, REGION_OPTIONS, hasSpaces, type FleetSize } from '../lib/fleetCatalog.ts'
import { Button, StatusChip, CopyButton, type StatusTone } from '../components/ui/index.ts'
import '../styles/fleet-builder.css'

const oddStep = (c: number, d: number) => (d > 0 ? Math.min(7, c + 2) : Math.max(3, c - 2))
const controlBStep = (c: number, d: number) => (d > 0 ? (c === 0 ? 3 : Math.min(7, c + 2)) : c <= 3 ? 0 : c - 2)
const appStep = (c: number, d: number) => Math.min(5, Math.max(1, c + d))
const toneFor = (s: FleetSeverity): StatusTone => (s === 'error' ? 'danger' : s === 'warning' ? 'warning' : 'info')

export function FleetBuilderPage() {
  const [draft, setDraft] = useState<FleetDraft>(defaultDraft)
  const [past, setPast] = useState<FleetDraft[]>([])
  const [future, setFuture] = useState<FleetDraft[]>([])
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [showYaml, setShowYaml] = useState(false)

  const mutate = useCallback((fn: (d: FleetDraft) => FleetDraft) => {
    setDraft(current => { setPast(p => [...p.slice(-99), current]); setFuture([]); return fn(current) })
  }, [])
  const undo = useCallback(() => {
    setPast(p => {
      if (p.length === 0) return p
      const prev = p[p.length - 1]
      setFuture(f => [draft, ...f]); setDraft(prev); return p.slice(0, -1)
    })
  }, [draft])
  const redo = useCallback(() => {
    setFuture(f => {
      if (f.length === 0) return f
      const next = f[0]
      setPast(p => [...p, draft]); setDraft(next); return f.slice(1)
    })
  }, [draft])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'z') { e.preventDefault(); e.shiftKey ? redo() : undo() }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [undo, redo])

  const { nodes: laidNodes, edges } = useMemo(() => layout(draft), [draft])
  const [nodes, setNodes, onNodesChange] = useNodesState<FleetFlowNode>(laidNodes)
  useEffect(() => { setNodes(laidNodes) }, [laidNodes, setNodes])

  const findings = useMemo(() => validateDraft(draft), [draft])
  const lines = useMemo(() => costLines(draft), [draft])
  const total = totalMonthlyUSD(draft)
  const blocking = findings.filter(f => f.severity === 'error').length
  const warnings = findings.filter(f => f.severity === 'warning').length

  // Region frames recompute from live node positions so they grow while nodes are dragged.
  const framedNodes = useMemo(() => [...regionFrameNodes(nodes, draft), ...nodes], [nodes, draft])
  const selected = framedNodes.find(n => n.id === selectedId) ?? null
  // Legend shows only the connector kinds actually present in the current graph.
  const legendKinds = useMemo(() => {
    const present = new Set(edges.map(e => (e.data as { kind?: EdgeKind } | undefined)?.kind).filter(Boolean) as EdgeKind[])
    return EDGE_ORDER.filter(k => present.has(k))
  }, [edges])
  const yaml = useMemo(() => clusterYaml(draft), [draft])

  const onNodeClick = useCallback<NodeMouseHandler>((_, node) => setSelectedId(node.id), [])
  const onNodeDragStop = useCallback((_: unknown, node: { id: string; position: { x: number; y: number } }) => {
    setDraft(d => ({ ...d, positions: { ...d.positions, [node.id]: node.position } }))
  }, [])

  const downloadYaml = () => {
    // One valid file per document (a cluster.yaml per region + an optional fleet-extras.yaml).
    for (const doc of fleetDocuments(draft)) {
      const url = URL.createObjectURL(new Blob([doc.yaml], { type: 'text/yaml' }))
      const a = document.createElement('a')
      a.href = url; a.download = doc.filename; a.click()
      URL.revokeObjectURL(url)
    }
  }

  return (
    <div className="fleet-builder-page">
      <div className="fleet-builder-toolbar">
        <Field label="Cluster">
          <input
            className="fleet-builder-name"
            type="text"
            value={draft.name}
            spellCheck={false}
            aria-label="Cluster name"
            onChange={e => { const v = e.currentTarget.value; setDraft(d => ({ ...d, name: v })) }}
          />
        </Field>
        <Field label={draft.regions === 2 ? 'Region A' : 'Region'}>
          <select value={draft.region} onChange={e => { const v = e.currentTarget.value; mutate(d => ({ ...d, region: v })) }}>
            {REGION_OPTIONS.map(r => <option key={r} value={r}>{r}{hasSpaces(r) ? '' : ' ⚠'}</option>)}
          </select>
        </Field>
        {draft.regions === 2 && (
          <Field label="Region B">
            <select value={draft.secondRegion} onChange={e => { const v = e.currentTarget.value; mutate(d => ({ ...d, secondRegion: v })) }}>
              {REGION_OPTIONS.map(r => <option key={r} value={r}>{r}{hasSpaces(r) ? '' : ' ⚠'}</option>)}
            </select>
          </Field>
        )}
        <Field label="Edge">
          <select value={draft.edge} onChange={e => { const v = e.currentTarget.value as FleetEdgeMode; mutate(d => ({ ...d, edge: v })) }}>
            <option value="none">None (LB only)</option>
            <option value="cloudflare">Cloudflare</option>
          </select>
        </Field>
        <Stepper label="Regions" value={draft.regions} onStep={s => mutate(d => ({ ...d, regions: Math.min(2, Math.max(1, d.regions + s)) }))} />
        <Stepper label={draft.regions === 2 ? 'Control·A' : 'Control'} value={draft.controlA} onStep={s => mutate(d => ({ ...d, controlA: oddStep(d.controlA, s) }))} />
        {draft.regions === 2 && <Stepper label="Control·B" value={draft.controlB} onStep={s => mutate(d => ({ ...d, controlB: controlBStep(d.controlB, s) }))} />}
        <Stepper label={draft.regions === 2 ? 'App·A' : 'App'} value={draft.appA} onStep={s => mutate(d => ({ ...d, appA: appStep(d.appA, s) }))} />
        {draft.regions === 2 && <Stepper label="App·B" value={draft.appB} onStep={s => mutate(d => ({ ...d, appB: appStep(d.appB, s) }))} />}
        <div className="fleet-builder-add">
          <Button size="sm" variant="secondary" icon="fa-bolt" onClick={() => mutate(addService('cache'))}>Cache</Button>
          <Button size="sm" variant="secondary" icon="fa-bars-staggered" onClick={() => mutate(addService('queue'))}>Queue</Button>
          <Button size="sm" variant="secondary" icon="fa-flask" onClick={() => mutate(addTestDB)}>Test DB</Button>
        </div>
        <div className="fleet-builder-toolbar-spacer" />
        <Button size="sm" variant={showYaml ? 'secondary' : 'ghost'} icon="fa-code" onClick={() => setShowYaml(v => !v)} aria-pressed={showYaml}>YAML</Button>
        <Button size="sm" variant="ghost" icon="fa-arrow-rotate-left" onClick={undo} disabled={past.length === 0} aria-label="Undo" />
        <Button size="sm" variant="ghost" icon="fa-arrow-rotate-right" onClick={redo} disabled={future.length === 0} aria-label="Redo" />
        <StatusChip tone={blocking ? 'danger' : warnings ? 'warning' : 'success'} label={blocking ? `${blocking} blocking` : warnings ? `${warnings} warning` : 'Valid'} />
      </div>

      <div className="fleet-builder-body">
        <div className="fleet-builder-canvas">
          <ReactFlow
            nodes={framedNodes as FleetFlowNode[]}
            edges={edges as Edge[]}
            nodeTypes={fleetNodeTypes}
            onNodesChange={onNodesChange}
            onNodeClick={onNodeClick}
            onNodeDragStop={onNodeDragStop}
            onPaneClick={() => setSelectedId(null)}
            fitView
            fitViewOptions={{ padding: 0.18 }}
            minZoom={0.3}
            maxZoom={1.5}
            proOptions={{ hideAttribution: true }}
          >
            <Background color="var(--topology-grid)" gap={24} />
            <Controls position="bottom-left" showInteractive={false} />
            <MiniMap position="bottom-right" pannable zoomable />
            {legendKinds.length > 0 && (
              <Panel position="bottom-center">
                <div className="fleet-legend">
                  {legendKinds.map(kind => (
                    <span className="fleet-legend-item" key={kind}>
                      <LegendSwatch kind={kind} />
                      <span>{EDGE_LABEL[kind]}</span>
                    </span>
                  ))}
                </div>
              </Panel>
            )}
          </ReactFlow>
        </div>

        {showYaml && (
          <section className="fleet-builder-yaml">
            <header className="fleet-builder-yaml-head">
              <i className="fawsb fa-code" aria-hidden="true" />
              <strong>cluster.yaml</strong>
              <span className="mono">norn.dev/fleet/v1</span>
              <div className="fleet-builder-toolbar-spacer" />
              <CopyButton value={yaml} label="Copy" />
            </header>
            <pre className="fleet-builder-yaml-body"><code>{yaml}</code></pre>
          </section>
        )}

        <aside className="fleet-builder-inspector">
          <Inspector selected={selected} draft={draft} mutate={mutate} />
          <section className="fleet-builder-panel">
            <h3>Validation</h3>
            <ul className="fleet-builder-findings">
              {findings.map(f => (
                <li key={`${f.code}:${f.field}`}>
                  <StatusDotForSeverity severity={f.severity} />
                  <div><span className="fleet-finding-msg">{f.message}</span>{f.remediation && <span className="fleet-finding-fix">{f.remediation}</span>}</div>
                </li>
              ))}
            </ul>
          </section>
          <section className="fleet-builder-panel">
            <h3>Estimated cost</h3>
            <ul className="fleet-builder-cost">
              {lines.map(l => (
                <li key={l.label}><span>{l.label}</span><span className="mono">{l.usdMonthly === 0 ? 'free' : `$${l.usdMonthly}`}</span></li>
              ))}
              <li className="fleet-builder-cost-total"><span>Total</span><span className="mono">${total}/mo</span></li>
            </ul>
          </section>
          <div className="fleet-builder-actions">
            <CopyButton value={yaml} label="Copy fleet YAML" />
            <Button variant="secondary" icon="fa-download" onClick={downloadYaml}>Export fleet YAML</Button>
          </div>
          <p className="fleet-builder-foot">{totalApps(draft)} app nodes · design-time preview · applying is done through the GitOps path</p>
        </aside>
      </div>
    </div>
  )
}

// MARK: mutation helpers

function addService(kind: FleetServiceKind) {
  return (d: FleetDraft): FleetDraft => ({
    ...d,
    services: [...d.services, {
      id: crypto.randomUUID(), kind,
      engine: kind === 'cache' ? 'Valkey' : 'Redpanda',
      size: kind === 'cache' ? 's-2vcpu-4gb' : 's-4vcpu-8gb',
      count: kind === 'cache' ? 1 : 3, x: 0, y: 0,
    }],
  })
}
function addTestDB(d: FleetDraft): FleetDraft {
  return { ...d, extras: [...d.extras, { id: crypto.randomUUID(), mode: 'self', engine: 'pg', size: 's-2vcpu-4gb', x: 0, y: 0 }] }
}

// MARK: small controls

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="fleet-builder-field"><span>{label}</span>{children}</label>
}
function Stepper({ label, value, onStep }: { label: string; value: number; onStep: (s: number) => void }) {
  return (
    <div className="fleet-builder-field">
      <span>{label}</span>
      <div className="fleet-builder-stepper">
        <button type="button" onClick={() => onStep(-1)} aria-label={`Decrease ${label}`}>–</button>
        <span className="mono">{value}</span>
        <button type="button" onClick={() => onStep(1)} aria-label={`Increase ${label}`}>+</button>
      </div>
    </div>
  )
}
function LegendSwatch({ kind }: { kind: EdgeKind }) {
  const s = EDGE_STYLE[kind]
  return (
    <svg className="fleet-legend-swatch" width={26} height={8} viewBox="0 0 26 8" aria-hidden="true">
      <line
        x1={1} y1={4} x2={25} y2={4}
        strokeLinecap="round"
        strokeDasharray={s.dash}
        className={s.animated ? 'marching' : undefined}
        style={{ stroke: s.stroke, strokeWidth: s.width }}
      />
    </svg>
  )
}
function StatusDotForSeverity({ severity }: { severity: FleetSeverity }) {
  const tone = toneFor(severity)
  return <span className={`ui-status-dot ui-status-${tone}`} aria-label={severity} />
}
function SizeSelect({ value, sizes, onChange }: { value: string; sizes: FleetSize[]; onChange: (v: string) => void }) {
  return (
    <select value={value} onChange={e => onChange(e.currentTarget.value)}>
      {sizes.map(s => <option key={s.slug} value={s.slug}>{s.slug} · {s.vcpu}vcpu/{s.memGB}gb · ${s.usdMonthly}/mo</option>)}
    </select>
  )
}
function Seg<T extends string>({ options, value, onChange, disabled }: { options: [T, string][]; value: T; onChange: (v: T) => void; disabled?: boolean }) {
  return (
    <div className="fleet-builder-seg" role="group">
      {options.map(([v, label]) => (
        <button key={v} type="button" aria-pressed={v === value} disabled={disabled} onClick={() => onChange(v)}>{label}</button>
      ))}
    </div>
  )
}

// MARK: inspector

function Inspector({ selected, draft, mutate }: {
  selected: FleetFlowNode | FleetRegionNode | null
  draft: FleetDraft
  mutate: (fn: (d: FleetDraft) => FleetDraft) => void
}) {
  if (!selected) {
    return (
      <section className="fleet-builder-panel">
        <h3>Fleet Builder</h3>
        <p className="fleet-builder-hint">Select a node to configure it, or click a region label to reassign it. Drag nodes to rearrange. Use the bar above to add regions, control planes, app pools, cache, queue, and databases.</p>
      </section>
    )
  }
  const kind = selected.data.kind
  if (kind === 'region') {
    return (
      <section className="fleet-builder-panel">
        <h3>{(selected.data as { region: number }).region === 0 ? 'Region A' : 'Region B'}</h3>
        <RegionEditor region={(selected.data as { region: number }).region} draft={draft} mutate={mutate} />
      </section>
    )
  }
  return (
    <section className="fleet-builder-panel">
      <h3>{inspectorTitle(kind)}</h3>
      {(kind === 'control') && (
        <>
          <label className="fleet-builder-row"><span>Node size</span><SizeSelect value={draft.sizes.control} sizes={NODE_SIZES} onChange={v => mutate(d => ({ ...d, sizes: { ...d.sizes, control: v } }))} /></label>
          <p className="fleet-builder-hint">Shared control-plane size across regions.</p>
        </>
      )}
      {(kind === 'app') && (
        <>
          <label className="fleet-builder-row"><span>Node size</span><SizeSelect value={draft.sizes.app} sizes={NODE_SIZES} onChange={v => mutate(d => ({ ...d, sizes: { ...d.sizes, app: v } }))} /></label>
          <p className="fleet-builder-hint">{draft.regions === 2 ? `A: ${draft.appA} · B: ${draft.appB} nodes` : `${draft.appA} nodes`}</p>
        </>
      )}
      {(kind === 'dbPrimary' || kind === 'dbReplica') && <DBEditor draft={draft} mutate={mutate} />}
      {kind === 'dbExtra' && selected.data.extraId && <TestDBEditor id={selected.data.extraId} draft={draft} mutate={mutate} />}
      {(kind === 'cache' || kind === 'queue') && selected.data.serviceId && <ServiceEditor id={selected.data.serviceId} draft={draft} mutate={mutate} />}
      {(kind === 'lb' || kind === 'edgeCloudflare' || kind === 'edgeDNS') && (
        <>
          <Seg options={[['none', 'None'], ['cloudflare', 'Cloudflare']]} value={draft.edge} onChange={v => mutate(d => ({ ...d, edge: v }))} />
          <p className="fleet-builder-hint">{draft.edge === 'cloudflare' ? 'Cloudflare → regional LB → apps. Global anycast/WAF; the DO LB stays the origin.' : 'DigitalOcean regional load balancer in front of the app pool.'}</p>
        </>
      )}
      {kind === 'spaces' && <p className="fleet-builder-hint">{hasSpaces(draft.region) ? `${draft.region} has DO Spaces for state + WAL backup.` : `${draft.region} has no DO Spaces — choose a Spaces region.`}</p>}
    </section>
  )
}

function RegionEditor({ region, draft, mutate }: { region: number; draft: FleetDraft; mutate: (fn: (d: FleetDraft) => FleetDraft) => void }) {
  const current = region === 0 ? draft.region : draft.secondRegion
  const duplicate = draft.regions === 2 && draft.region === draft.secondRegion
  const setRegion = (v: string) => mutate(d => (region === 0 ? { ...d, region: v } : { ...d, secondRegion: v }))
  return (
    <>
      <label className="fleet-builder-row">
        <span>Region</span>
        <select value={current} onChange={e => { const v = e.currentTarget.value; setRegion(v) }}>
          {REGION_OPTIONS.map(r => <option key={r} value={r}>{r}{hasSpaces(r) ? '' : ' ⚠ no Spaces'}</option>)}
        </select>
      </label>
      {duplicate ? (
        <p className="fleet-builder-hint" style={{ color: 'var(--warn)' }}>
          Region A and B are both {current} — a second region should be distinct for HA. Pick another (you can override).
        </p>
      ) : !hasSpaces(current) ? (
        <p className="fleet-builder-hint">{current} has no DO Spaces — state + WAL backup need a Spaces region.</p>
      ) : (
        <p className="fleet-builder-hint">{region === 0 ? 'Primary region — hosts the managed/self database.' : 'Secondary region for the HA app pool.'}</p>
      )}
    </>
  )
}

function DBEditor({ draft, mutate }: { draft: FleetDraft; mutate: (fn: (d: FleetDraft) => FleetDraft) => void }) {
  const managed = draft.db.mode === 'managed'
  return (
    <>
      <Seg options={[['self', 'Self-managed'], ['managed', 'Managed']]} value={draft.db.mode} onChange={(v: FleetDBMode) => mutate(d => ({ ...d, db: { ...d.db, mode: v, ...(v === 'self' ? { engine: 'pg' as FleetDBEngine, replica: false } : {}) } }))} />
      <Seg options={[['pg', 'PostgreSQL'], ['mysql', 'MySQL']]} value={draft.db.engine} onChange={(v: FleetDBEngine) => mutate(d => ({ ...d, db: { ...d.db, engine: v } }))} disabled={!managed} />
      {managed ? (
        <>
          <label className="fleet-builder-row"><span>Size</span><SizeSelect value={draft.db.managedSize} sizes={MANAGED_SIZES} onChange={v => mutate(d => ({ ...d, db: { ...d.db, managedSize: v } }))} /></label>
          <label className="fleet-builder-toggle"><input type="checkbox" checked={draft.db.replica} onChange={e => mutate(d => ({ ...d, db: { ...d.db, replica: e.currentTarget.checked } }))} /> Read replica</label>
          {draft.db.replica && draft.regions === 2 && (
            <Seg options={[['same', 'Same region'], ['b', 'Region B']]} value={draft.db.replicaRegion} onChange={(v: FleetReplicaRegion) => mutate(d => ({ ...d, db: { ...d.db, replicaRegion: v } }))} />
          )}
          <p className="fleet-builder-hint">Provider-run HA, backups &amp; failover. Replicas default to the primary's region.</p>
        </>
      ) : (
        <>
          <label className="fleet-builder-row"><span>Size</span><SizeSelect value={draft.db.selfSize} sizes={NODE_SIZES} onChange={v => mutate(d => ({ ...d, db: { ...d.db, selfSize: v } }))} /></label>
          <p className="fleet-builder-hint">Patroni ×3 in-VPC HA. Managed is recommended for production.</p>
        </>
      )}
    </>
  )
}

function TestDBEditor({ id, draft, mutate }: { id: string; draft: FleetDraft; mutate: (fn: (d: FleetDraft) => FleetDraft) => void }) {
  const extra = draft.extras.find(e => e.id === id)
  if (!extra) return null
  const update = (fn: (e: FleetDraft['extras'][number]) => FleetDraft['extras'][number]) =>
    mutate(d => ({ ...d, extras: d.extras.map(e => (e.id === id ? fn(e) : e)) }))
  const managed = extra.mode === 'managed'
  return (
    <>
      <Seg options={[['self', 'Self-managed'], ['managed', 'Managed']]} value={extra.mode} onChange={(v: FleetDBMode) => update(e => ({ ...e, mode: v, engine: v === 'self' ? 'pg' : e.engine, size: v === 'managed' ? 'db-s-2vcpu-4gb' : 's-2vcpu-4gb' }))} />
      <Seg options={[['pg', 'PostgreSQL'], ['mysql', 'MySQL']]} value={extra.engine} onChange={(v: FleetDBEngine) => update(e => ({ ...e, engine: v }))} disabled={!managed} />
      <label className="fleet-builder-row"><span>Size</span><SizeSelect value={extra.size} sizes={managed ? MANAGED_SIZES : NODE_SIZES} onChange={v => update(e => ({ ...e, size: v }))} /></label>
      <Button size="sm" variant="danger" onClick={() => mutate(d => ({ ...d, extras: d.extras.filter(e => e.id !== id) }))}>Remove pool</Button>
    </>
  )
}

function ServiceEditor({ id, draft, mutate }: { id: string; draft: FleetDraft; mutate: (fn: (d: FleetDraft) => FleetDraft) => void }) {
  const svc = draft.services.find(s => s.id === id)
  if (!svc) return null
  const update = (fn: (s: FleetDraft['services'][number]) => FleetDraft['services'][number]) =>
    mutate(d => ({ ...d, services: d.services.map(s => (s.id === id ? fn(s) : s)) }))
  const engines = svc.kind === 'cache' ? ['Valkey', 'Redis'] : ['Redpanda', 'Kafka']
  return (
    <>
      <div className="fleet-builder-seg" role="group">
        {engines.map(en => <button key={en} type="button" aria-pressed={svc.engine === en} onClick={() => update(s => ({ ...s, engine: en }))}>{en}</button>)}
      </div>
      <label className="fleet-builder-row"><span>Node size</span><SizeSelect value={svc.size} sizes={NODE_SIZES} onChange={v => update(s => ({ ...s, size: v }))} /></label>
      <Stepper label="Nodes" value={svc.count} onStep={n => update(s => ({ ...s, count: Math.min(9, Math.max(1, s.count + n)) }))} />
      <p className="fleet-builder-hint">Runs on its own nodes with durable storage — independent of the stateless app pool.</p>
      <Button size="sm" variant="danger" onClick={() => mutate(d => ({ ...d, services: d.services.filter(s => s.id !== id) }))}>Remove pool</Button>
    </>
  )
}

function inspectorTitle(kind: string): string {
  switch (kind) {
    case 'control': return 'Control plane'
    case 'app': return 'App pool'
    case 'dbPrimary': return 'Database'
    case 'dbReplica': return 'Read replica'
    case 'dbExtra': return 'Test database'
    case 'cache': return 'Cache'
    case 'queue': return 'Queue'
    case 'spaces': return 'Object storage'
    default: return 'Ingress'
  }
}

export default FleetBuilderPage
