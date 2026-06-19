import { useMemo, useState, type CSSProperties } from 'react'
import {
  Background,
  Controls,
  Handle,
  MarkerType,
  MiniMap,
  Position,
  ReactFlow,
  type Edge,
  type Node,
  type NodeProps,
} from '@xyflow/react'
import type { AccessPattern, AppStatus, InfraSpec, ServiceManifest, ServiceManifestEntry } from '../types/index.ts'

type TopologyScope = 'public' | 'tailnet' | 'lan' | 'local' | 'internal'
type TopologyKind = 'origin' | 'ingress' | 'gateway' | 'service' | 'allocation' | 'dependency'

interface TopologyNodeData extends Record<string, unknown> {
  label: string
  eyebrow: string
  kind: TopologyKind
  scope: TopologyScope
  status?: string
  detail?: string
  address?: string
  addresses?: string[]
  meta?: string[]
}

type TopologyNodeType = Node<TopologyNodeData, 'topology'>

interface TopologyViewProps {
  apps: AppStatus[]
  serviceManifest: ServiceManifest | null
  accessPatterns: AccessPattern[]
  activeIngress: Set<string>
}

const scopeColors: Record<TopologyScope, string> = {
  public: '#f97316',
  tailnet: '#14b8a6',
  lan: '#eab308',
  local: '#64748b',
  internal: '#8b5cf6',
}

const scopeLabels: Record<TopologyScope, string> = {
  public: 'Public',
  tailnet: 'Tailnet',
  lan: 'LAN',
  local: 'Local',
  internal: 'Internal',
}

const kindLabels: Record<TopologyKind, string> = {
  origin: 'Origin',
  ingress: 'Ingress',
  gateway: 'Gateway',
  service: 'Service',
  allocation: 'Runtime',
  dependency: 'Dependency',
}

const columnX: Record<TopologyKind, number> = {
  origin: 0,
  ingress: 300,
  gateway: 590,
  service: 900,
  allocation: 1180,
  dependency: 1460,
}

function TopologyNode({ data, selected }: NodeProps<TopologyNodeType>) {
  const color = scopeColors[data.scope]
  return (
    <div className={`topology-node topology-node-${data.kind} ${selected ? 'selected' : ''}`} style={{ '--node-color': color } as CSSProperties}>
      <Handle type="target" position={Position.Left} className="topology-handle" />
      <div className="topology-node-topline">
        <span className="topology-node-eyebrow">{data.eyebrow}</span>
        <span className={`topology-node-status ${data.status === 'critical' || data.status === 'failed' ? 'bad' : ''}`} />
      </div>
      <div className="topology-node-label">{data.label}</div>
      {data.detail && <div className="topology-node-detail">{data.detail}</div>}
      {data.address && <div className="topology-node-address">{data.address}</div>}
      {data.meta && data.meta.length > 0 && (
        <div className="topology-node-meta">
          {data.meta.slice(0, 3).map(item => <span key={item}>{item}</span>)}
        </div>
      )}
      <Handle type="source" position={Position.Right} className="topology-handle" />
    </div>
  )
}

const nodeTypes = { topology: TopologyNode }

function endpointHost(raw: string): string {
  const trimmed = raw.trim()
  if (!trimmed) return ''
  try {
    const parsed = new URL(trimmed)
    return parsed.hostname.toLowerCase()
  } catch {
    if (trimmed.includes('://') || /[/#?]/.test(trimmed)) return ''
    const withoutPort = trimmed.replace(/^\[/, '').replace(/\](:\d+)?$/, '').replace(/:\d+$/, '')
    return withoutPort.replace(/\.$/, '').toLowerCase()
  }
}

function isIPv4(host: string): boolean {
  return /^\d{1,3}(\.\d{1,3}){3}$/.test(host)
}

function isTailnetIP(host: string): boolean {
  if (!isIPv4(host)) return false
  const [first, second] = host.split('.').map(Number)
  return first === 100 && (second & 0xc0) === 64
}

function isLanIP(host: string): boolean {
  if (!isIPv4(host)) return false
  const [first, second] = host.split('.').map(Number)
  return first === 10 || (first === 172 && second >= 16 && second <= 31) || (first === 192 && second === 168)
}

function endpointScope(raw: string, activeIngress: Set<string>): TopologyScope {
  const host = endpointHost(raw)
  if (!host || host === 'localhost' || host === '127.0.0.1' || host === '::1') return 'local'
  if (host.endsWith('.ts.net') || host.endsWith('.norn') || isTailnetIP(host)) return 'tailnet'
  if (isLanIP(host)) return 'lan'
  if (activeIngress.has(host)) return 'public'
  return 'public'
}

function serviceScopes(service: ServiceManifestEntry, activeIngress: Set<string>): TopologyScope[] {
  const scopes = new Set<TopologyScope>()
  for (const endpoint of service.endpoints ?? []) {
    scopes.add(endpointScope(endpoint.url, activeIngress))
  }
  if (scopes.size === 0) {
    const exposure = service.reachability?.exposure
    if (exposure === 'local') scopes.add('local')
    else if (exposure === 'private') scopes.add('tailnet')
    else if (exposure === 'public') scopes.add('public')
    else scopes.add('internal')
  }
  return [...scopes]
}

function servicePrimaryScope(service: ServiceManifestEntry, activeIngress: Set<string>): TopologyScope {
  const scopes = serviceScopes(service, activeIngress)
  return scopes.includes('public') ? 'public' : scopes.includes('tailnet') ? 'tailnet' : scopes.includes('lan') ? 'lan' : scopes[0] ?? 'internal'
}

function publicIngressCount(activeIngress: Set<string>): number {
  return publicIngressHosts(activeIngress).length
}

function publicIngressHosts(activeIngress: Set<string>): string[] {
  const hosts: string[] = []
  for (const hostname of activeIngress) {
    if (endpointScope(hostname, new Set()) === 'public') hosts.push(hostname)
  }
  return hosts.sort()
}

function controlPlaneAddress(): string {
  if (typeof window === 'undefined' || !window.location.host) return 'dashboard host'
  return window.location.host
}

function routeAddresses(scope: TopologyScope, services: ServiceManifestEntry[], activeIngress: Set<string>): string[] {
  const addresses = new Set<string>()
  for (const service of services) {
    for (const endpoint of service.endpoints ?? []) {
      if (endpointScope(endpoint.url, activeIngress) === scope) addresses.add(endpoint.url)
    }
    if (scope === 'local') {
      for (const instance of service.instances ?? []) {
        if (instance.address && instance.port > 0) addresses.add(`${instance.address}:${instance.port}`)
      }
    }
  }
  return [...addresses].sort()
}

function ingressAddresses(scope: TopologyScope, services: ServiceManifestEntry[], activeIngress: Set<string>): string[] {
  if (scope === 'public') return publicIngressHosts(activeIngress)
  return routeAddresses(scope, services, activeIngress)
}

function firstAddress(addresses: string[], fallback: string): string {
  return addresses[0] ?? fallback
}

function moreAddressCount(addresses: string[]): string[] {
  return addresses.length > 1 ? [`+${addresses.length - 1} more`] : []
}

function instanceAddresses(service: ServiceManifestEntry): string[] {
  return (service.instances ?? [])
    .filter(instance => instance.address && instance.port > 0)
    .map(instance => `${instance.address}:${instance.port}`)
}

function appDependencies(spec: InfraSpec): Array<{ id: string; label: string; detail: string }> {
  const deps: Array<{ id: string; label: string; detail: string }> = []
  const infra = spec.infrastructure
  if (infra?.postgres) deps.push({ id: 'postgres', label: 'Postgres', detail: infra.postgres.database })
  if (infra?.redis) deps.push({ id: 'redis', label: 'Redis', detail: infra.redis.namespace ?? 'namespace' })
  if (infra?.nats) deps.push({ id: 'nats', label: 'NATS', detail: `${infra.nats.streams?.length ?? 0} streams` })
  if (infra?.kafka) deps.push({ id: 'kafka', label: 'Kafka', detail: `${infra.kafka.topics?.length ?? 0} topics` })
  if (infra?.objectStorage) deps.push({
    id: 'object-storage',
    label: 'Object storage',
    detail: `${infra.objectStorage.buckets?.length ?? 0} buckets`,
  })
  for (const service of spec.services ?? []) {
    deps.push({ id: `service-${service}`, label: service, detail: 'declared service' })
  }
  return deps
}

function edge(id: string, source: string, target: string, scope: TopologyScope, label?: string): Edge {
  return {
    id,
    source,
    target,
    label,
    type: 'smoothstep',
    animated: scope === 'public' || scope === 'tailnet',
    markerEnd: { type: MarkerType.ArrowClosed, color: scopeColors[scope] },
    style: { stroke: scopeColors[scope], strokeWidth: scope === 'internal' ? 2 : 3 },
    labelStyle: { fill: '#475569', fontSize: 11, fontWeight: 700 },
    labelBgStyle: { fill: '#f8fafc', fillOpacity: 0.92 },
  }
}

function scopeY(scope: TopologyScope): number {
  return { public: 0, tailnet: 125, lan: 250, local: 375, internal: 500 }[scope]
}

function serviceY(index: number): number {
  return index * 112
}

function buildTopology(
  apps: AppStatus[],
  manifest: ServiceManifest | null,
  activeIngress: Set<string>,
  enabledScopes: Set<TopologyScope>,
  selectedApp: string,
): { nodes: TopologyNodeType[]; edges: Edge[] } {
  const appSet = new Set(apps.map(app => app.spec.name))
  const appMap = new Map(apps.map(app => [app.spec.name, app]))
  const services = (manifest?.services ?? [])
    .filter(service => appSet.has(service.app))
    .filter(service => selectedApp === 'all' || service.app === selectedApp)
    .filter(service => serviceScopes(service, activeIngress).some(scope => enabledScopes.has(scope)))
  const nodes = new Map<string, TopologyNodeType>()
  const edges: Edge[] = []
  const publicHosts = publicIngressCount(activeIngress)
  const routeScopes = new Set<TopologyScope>()
  for (const service of services) {
    for (const scope of serviceScopes(service, activeIngress)) {
      if (enabledScopes.has(scope)) routeScopes.add(scope)
    }
  }
  if (services.length === 0) {
    for (const scope of enabledScopes) routeScopes.add(scope)
  }

  const addNode = (node: TopologyNodeType) => {
    if (!nodes.has(node.id)) nodes.set(node.id, node)
  }

  for (const scope of routeScopes) {
    const y = scopeY(scope)
    const originId = `origin-${scope}`
    const ingressId = `ingress-${scope}`
    const addresses = ingressAddresses(scope, services, activeIngress)
    addNode({
      id: originId,
      type: 'topology',
      position: { x: columnX.origin, y },
      data: {
        label: scope === 'public' ? 'Internet clients' : scope === 'tailnet' ? 'Tailnet devices' : scope === 'lan' ? 'LAN clients' : scope === 'local' ? 'Loopback' : 'Internal callers',
        eyebrow: kindLabels.origin,
        kind: 'origin',
        scope,
        detail: scopeLabels[scope],
        address: scope === 'public' ? '0.0.0.0/0' : scope === 'tailnet' ? '100.64.0.0/10' : scope === 'lan' ? 'RFC1918' : scope === 'local' ? '127.0.0.1' : 'cluster',
        addresses: scope === 'tailnet' ? ['100.64.0.0/10', '*.ts.net', '*.norn'] : undefined,
        meta: scope === 'public' ? ['Cloudflare edge'] : scope === 'tailnet' ? ['Tailscale DNS', '100.x routes'] : [],
      },
    })
    addNode({
      id: ingressId,
      type: 'topology',
      position: { x: columnX.ingress, y },
      data: {
        label: scope === 'public' ? 'Cloudflare tunnel' : scope === 'tailnet' ? 'Tailscale Serve' : scope === 'lan' ? 'LAN listener' : scope === 'local' ? 'Local port' : 'Service call',
        eyebrow: kindLabels.ingress,
        kind: 'ingress',
        scope,
        detail: scope === 'public' ? `${publicHosts} public hosts` : scopeLabels[scope],
        address: firstAddress(addresses, scope === 'public' ? 'cloudflared' : scope === 'tailnet' ? 'tailscale serve' : scope === 'lan' ? 'LAN bind' : scope === 'local' ? controlPlaneAddress() : 'service mesh'),
        addresses,
        meta: moreAddressCount(addresses),
      },
    })
    edges.push(edge(`route-${scope}-origin-ingress`, originId, ingressId, scope, scopeLabels[scope]))
  }

  const gatewayId = 'gateway-wake'
  addNode({
    id: gatewayId,
    type: 'topology',
    position: { x: columnX.gateway, y: 188 },
    data: {
      label: 'Wake gateway',
      eyebrow: kindLabels.gateway,
      kind: 'gateway',
      scope: 'tailnet',
      status: 'passing',
      detail: 'Norn route broker',
      address: `${controlPlaneAddress()}/api/wake-gateway`,
      addresses: [`${controlPlaneAddress()}/api/wake-gateway/{endpoint}`],
      meta: ['Host + /api/wake-gateway', 'scale-to-ready'],
    },
  })

  for (const scope of routeScopes) {
    edges.push(edge(`route-${scope}-ingress-gateway`, `ingress-${scope}`, gatewayId, scope))
  }

  services.forEach((service, index) => {
    const scope = servicePrimaryScope(service, activeIngress)
    const app = appMap.get(service.app)
    const serviceId = `service-${service.app}-${service.process}`
    const instances = service.instances ?? []
    const endpoints = service.endpoints?.map(endpoint => endpoint.url) ?? []
    const runtimeAddresses = instanceAddresses(service)
    addNode({
      id: serviceId,
      type: 'topology',
      position: { x: columnX.service, y: serviceY(index) },
      data: {
        label: `${service.app} / ${service.process}`,
        eyebrow: kindLabels.service,
        kind: 'service',
        scope,
        status: service.status,
        detail: `${service.type} · ${service.reachability.exposure}`,
        address: firstAddress(endpoints, runtimeAddresses[0] ?? service.healthPath ?? 'no endpoint'),
        addresses: endpoints.length > 0 ? endpoints : runtimeAddresses,
        meta: endpoints.length > 0 ? moreAddressCount(endpoints) : [service.healthPath ?? 'no endpoint'],
      },
    })
    edges.push(edge(`route-${serviceId}`, gatewayId, serviceId, scope, service.type))

    if (instances.length > 0) {
      const allocationId = `alloc-${service.app}-${service.process}`
      const passing = instances.filter(instance => instance.status === 'passing').length
      addNode({
        id: allocationId,
        type: 'topology',
        position: { x: columnX.allocation, y: serviceY(index) },
        data: {
          label: 'Nomad allocation',
          eyebrow: kindLabels.allocation,
          kind: 'allocation',
          scope: service.reachability.instanceScope === 'local' ? 'local' : 'lan',
          status: passing > 0 ? 'passing' : service.status,
          detail: `${passing}/${instances.length} passing`,
          address: firstAddress(runtimeAddresses, 'no routable instance'),
          addresses: runtimeAddresses,
          meta: moreAddressCount(runtimeAddresses),
        },
      })
      edges.push(edge(`runtime-${serviceId}`, serviceId, allocationId, 'internal', 'runs on'))
    }

    for (const dep of app ? appDependencies(app.spec) : []) {
      const depId = `dep-${service.app}-${dep.id}`
      addNode({
        id: depId,
        type: 'topology',
        position: { x: columnX.dependency, y: serviceY(index) + 48 },
        data: {
          label: dep.label,
          eyebrow: kindLabels.dependency,
          kind: 'dependency',
          scope: 'internal',
          detail: dep.detail,
          address: dep.detail,
        },
      })
      edges.push(edge(`dep-${serviceId}-${dep.id}`, serviceId, depId, 'internal'))
    }
  })

  return { nodes: [...nodes.values()], edges }
}

function selectedNodeFor(nodes: TopologyNodeType[], id: string | null): TopologyNodeType | null {
  if (!id) return null
  return nodes.find(node => node.id === id) ?? null
}

export function TopologyView({ apps, serviceManifest, accessPatterns, activeIngress }: TopologyViewProps) {
  const preferredApp = apps.some(app => app.spec.name === 'ft-trove') ? 'ft-trove' : apps[0]?.spec.name ?? 'all'
  const [selectedApp, setSelectedApp] = useState('')
  const [enabledScopes, setEnabledScopes] = useState<Set<TopologyScope>>(new Set(['public', 'tailnet', 'lan', 'local', 'internal']))
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null)
  const effectiveSelectedApp = selectedApp || preferredApp
  const { nodes, edges } = useMemo(
    () => buildTopology(apps, serviceManifest, activeIngress, enabledScopes, effectiveSelectedApp),
    [activeIngress, apps, effectiveSelectedApp, enabledScopes, serviceManifest],
  )
  const selectedNode = selectedNodeFor(nodes, selectedNodeId)
  const activeAppCount = effectiveSelectedApp === 'all' ? apps.length : 1
  const idleCandidates = accessPatterns.filter(pattern => pattern.idleCandidate).length

  const toggleScope = (scope: TopologyScope) => {
    setEnabledScopes(prev => {
      const next = new Set(prev)
      if (next.has(scope)) next.delete(scope)
      else next.add(scope)
      return next.size === 0 ? prev : next
    })
  }

  return (
    <section className="topology-view">
      <div className="topology-toolbar">
        <div>
          <h2>Topology</h2>
          <p>{activeAppCount} app{activeAppCount !== 1 ? 's' : ''} · {nodes.length} nodes · {edges.length} routes · {idleCandidates} idle candidates</p>
        </div>
        <div className="topology-controls">
          <label>
            <span>App</span>
            <select value={effectiveSelectedApp} onChange={event => setSelectedApp(event.target.value)}>
              <option value="all">All apps</option>
              {apps.map(app => <option key={app.spec.name} value={app.spec.name}>{app.spec.name}</option>)}
            </select>
          </label>
          <div className="topology-scope-controls" aria-label="Topology scopes">
            {(Object.keys(scopeLabels) as TopologyScope[]).map(scope => (
              <button
                key={scope}
                className={`topology-scope topology-scope-${scope} ${enabledScopes.has(scope) ? 'active' : ''}`}
                onClick={() => toggleScope(scope)}
                type="button"
              >
                <span />
                {scopeLabels[scope]}
              </button>
            ))}
          </div>
        </div>
      </div>

      <div className="topology-shell">
        <div className="topology-canvas" role="application" aria-label="Interactive Norn traffic topology">
          <ReactFlow
            key={`${effectiveSelectedApp}-${[...enabledScopes].sort().join('-')}-${nodes.length}`}
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            fitView
            fitViewOptions={{ padding: 0.18 }}
            minZoom={0.35}
            maxZoom={1.5}
            nodesDraggable
            onNodeClick={(_, node) => setSelectedNodeId(node.id)}
            onPaneClick={() => setSelectedNodeId(null)}
            proOptions={{ hideAttribution: true }}
          >
            <Background color="#dbe3ef" gap={24} />
            <Controls position="bottom-left" />
            <MiniMap position="bottom-right" nodeColor={node => scopeColors[(node.data.scope as TopologyScope) ?? 'internal']} pannable zoomable />
          </ReactFlow>
        </div>
        <aside className="topology-inspector">
          {selectedNode ? (
            <>
              <div className="topology-inspector-kicker">{selectedNode.data.eyebrow}</div>
              <h3>{selectedNode.data.label}</h3>
              <div className={`topology-inspector-scope scope-${selectedNode.data.scope}`}>
                <span />
                {scopeLabels[selectedNode.data.scope]}
              </div>
              {selectedNode.data.detail && <p>{selectedNode.data.detail}</p>}
              {selectedNode.data.address && (
                <div className="topology-inspector-address">
                  <span>Address</span>
                  <code>{selectedNode.data.address}</code>
                </div>
              )}
              {selectedNode.data.addresses && selectedNode.data.addresses.length > 1 && (
                <div className="topology-inspector-list">
                  {selectedNode.data.addresses.map(item => <code key={item}>{item}</code>)}
                </div>
              )}
              {selectedNode.data.meta && selectedNode.data.meta.length > 0 && (
                <div className="topology-inspector-list">
                  {selectedNode.data.meta.map(item => <code key={item}>{item}</code>)}
                </div>
              )}
            </>
          ) : (
            <>
              <div className="topology-inspector-kicker">Map key</div>
              <h3>Traffic shape</h3>
              <p>Select a station to inspect its listener, route, runtime, or dependency details.</p>
              <div className="topology-legend">
                {(Object.keys(scopeLabels) as TopologyScope[]).map(scope => (
                  <span key={scope} className={`scope-${scope}`}><i />{scopeLabels[scope]}</span>
                ))}
              </div>
            </>
          )}
        </aside>
      </div>
    </section>
  )
}
