const now = Date.now()

const minutesAgo = (n) => new Date(now - n * 60_000).toISOString()

const allocation = (id, taskGroup, overrides = {}) => ({
  id,
  taskGroup,
  status: 'running',
  lifecycle: 'active',
  healthy: true,
  nodeName: 'mini',
  nodeAddress: '192.0.2.40',
  nodeProvider: 'local',
  startedAt: minutesAgo(84),
  ...overrides,
})

export const deploySteps = [
  { step: 'clone', status: 'done', message: 'Checked out signal-sideband at 8f42ac1.' },
  { step: 'build', status: 'done', message: 'Built image signal-sideband:8f42ac1.' },
  { step: 'test', status: 'done', message: 'go test ./... passed.' },
  { step: 'snapshot', status: 'done', message: 'Captured the pre-deploy database snapshot.' },
  { step: 'migrate', status: 'done', message: 'No pending migrations.' },
  { step: 'submit', status: 'done', message: 'Submitted the Nomad job update.' },
  { step: 'healthy', status: 'running', message: 'Waiting for the replacement allocation to become healthy.' },
]

export const apps = [
  {
    spec: {
      name: 'signal-sideband',
      deploy: true,
      repo: {
        url: 'git@github.com:example/signal-sideband.git',
        branch: 'main',
        autoDeploy: true,
      },
      build: { dockerfile: 'Dockerfile', test: 'go test ./...' },
      primaryRegion: 'ord',
      regions: {
        ord: { nomadRegion: 'global', datacenters: ['ord'], trafficWeight: 100 },
        nyc: { nomadRegion: 'global', datacenters: ['nyc'], trafficWeight: 0 },
      },
      placement: { nodePool: 'app' },
      processes: {
        web: {
          port: 8080,
          command: './sideband serve',
          health: { path: '/healthz', interval: '15s', timeout: '3s' },
          scaling: { min: 2, max: 4 },
          resources: { cpu: 500, memory: 512 },
        },
        'media-worker': {
          command: './sideband media-worker',
          scaling: { min: 1, max: 2 },
          resources: { cpu: 300, memory: 384 },
        },
      },
      infrastructure: {
        postgres: { database: 'signal_sideband' },
        redis: { namespace: 'signal-sideband' },
        kafka: { topics: ['signal.received', 'signal.replayed'] },
        objectStorage: { provider: 'garage', buckets: [{ name: 'signal-sideband-media', access: 'private' }] },
      },
      services: ['postgres', 'redis', 'redpanda', 'garage'],
      secrets: ['SIGNAL_NUMBER', 'DATABASE_URL', 'GARAGE_ACCESS_KEY'],
      migrations: './sideband migrate',
      snapshots: { keep: 3, preRestore: true, retentionEnabled: true, exportBucket: 'signal-sideband-backups' },
      endpoints: [{ url: 'https://sideband.example.com' }],
    },
    nomadStatus: 'running',
    healthy: false,
    allocations: [
      allocation('7f612c93', 'web'),
      allocation('f184e212', 'web', { healthy: false, status: 'pending', startedAt: minutesAgo(6) }),
      allocation('e9ad3021', 'media-worker'),
    ],
    allocationSummary: {
      running: 2,
      active: 3,
      retained: 0,
      total: 3,
      byProcess: {
        web: { running: 1, active: 2, retained: 0, total: 2 },
        'media-worker': { running: 1, active: 1, retained: 0, total: 1 },
      },
    },
  },
  {
    spec: {
      name: 'contextdb',
      deploy: true,
      repo: { url: 'git@github.com:example/contextdb.git', branch: 'main', autoDeploy: true },
      processes: {
        web: {
          port: 8787,
          command: './contextdb serve',
          health: { path: '/health' },
          resources: { cpu: 400, memory: 768 },
        },
        reviewer: { command: './contextdb review', resources: { cpu: 300, memory: 512 } },
      },
      infrastructure: {
        postgres: { database: 'contextdb' },
        kafka: { topics: ['claims.reviewed', 'claims.promoted'] },
      },
      services: ['postgres', 'redpanda'],
      secrets: ['DATABASE_URL', 'MODEL_API_KEY'],
      endpoints: [{ url: 'https://context.example.com' }],
    },
    nomadStatus: 'running',
    healthy: true,
    allocations: [allocation('ea34bc19', 'web', { startedAt: minutesAgo(36) }), allocation('54c29e08', 'reviewer', { startedAt: minutesAgo(36) })],
    allocationSummary: { running: 2, active: 2, retained: 0, total: 2 },
  },
  {
    spec: {
      name: 'field-harbor-digest',
      deploy: true,
      repo: { url: 'git@github.com:example/field-harbor.git', branch: 'main', autoDeploy: true },
      processes: {
        digest: {
          command: './field-harbor digest',
          schedule: '0 7 * * *',
          resources: { cpu: 250, memory: 384 },
        },
      },
      infrastructure: {
        postgres: { database: 'field_harbor' },
        objectStorage: { provider: 'garage', buckets: [{ name: 'field-harbor-archive', access: 'private' }] },
      },
      services: ['postgres', 'garage'],
      secrets: ['DATABASE_URL', 'GARAGE_SECRET_KEY'],
    },
    nomadStatus: 'running',
    healthy: true,
    allocations: [],
    allocationSummary: { running: 0, active: 0, retained: 0, total: 0 },
  },
  {
    spec: {
      name: 'archive-thumb',
      deploy: true,
      repo: { url: 'git@github.com:example/archive-tools.git', branch: 'main' },
      processes: {
        render: {
          command: './thumb render',
          function: { timeout: '10m', memory: 1024 },
          resources: { cpu: 500, memory: 1024 },
        },
      },
      infrastructure: {
        objectStorage: { provider: 'garage', buckets: [{ name: 'archive-renders', access: 'private' }] },
      },
      services: ['garage'],
      secrets: ['GARAGE_ACCESS_KEY'],
    },
    nomadStatus: 'running',
    healthy: true,
    allocations: [],
    allocationSummary: { running: 0, active: 0, retained: 0, total: 0 },
  },
]

export const deployments = [
  {
    id: 'dep-signal-sideband-8f42ac1',
    app: 'signal-sideband',
    commitSha: '8f42ac18d8ff7bb26b33a23fd63113360527bca9',
    imageTag: 'signal-sideband:8f42ac1',
    sagaId: 'saga-812f4c19',
    status: 'deploying',
    sourceKind: 'operator',
    sourceRef: 'HEAD',
    startedAt: minutesAgo(4),
  },
  {
    id: 'dep-signal-sideband-1c1a9c1',
    app: 'signal-sideband',
    commitSha: '1c1a9c1d4b6a7c1eaf01b1ba734ea087948b13ee',
    imageTag: 'signal-sideband:1c1a9c1',
    sagaId: 'saga-810c739a',
    status: 'failed',
    sourceKind: 'webhook',
    sourceRef: 'main',
    startedAt: minutesAgo(83),
    finishedAt: minutesAgo(79),
  },
  {
    id: 'dep-contextdb-b7fb419',
    app: 'contextdb',
    commitSha: 'b7fb419b3150de885a62f51464d8152f8af01c2d',
    imageTag: 'contextdb:b7fb419',
    sagaId: 'saga-809b7e12',
    status: 'deployed',
    sourceKind: 'webhook',
    sourceRef: 'main',
    startedAt: minutesAgo(36),
    finishedAt: minutesAgo(35),
  },
  {
    id: 'dep-field-harbor-de71b90',
    app: 'field-harbor-digest',
    commitSha: 'de71b901a99c1deca91f41fe89c4710b0d33d778',
    imageTag: 'field-harbor-digest:de71b90',
    sagaId: 'saga-8041de71',
    status: 'deployed',
    startedAt: minutesAgo(140),
    finishedAt: minutesAgo(139),
  },
]

export const events = {
  total: 4,
  events: [
    {
      id: 'evt-401',
      source: 'nomad',
      app: 'signal-sideband',
      environment: 'production',
      type: 'allocation.unhealthy',
      severity: 'critical',
      state: 'open',
      title: 'Web allocation is repeatedly restarting',
      body: 'One of two web allocations failed its health gate.',
      dedupeKey: 'signal-sideband:web:health',
      occurredAt: minutesAgo(6),
    },
    {
      id: 'evt-400',
      source: 'norn',
      app: 'signal-sideband',
      environment: 'production',
      type: 'deploy.failed',
      severity: 'warning',
      state: 'open',
      title: 'Previous deployment failed its health gate',
      occurredAt: minutesAgo(79),
    },
    {
      id: 'evt-398',
      source: 'norn',
      app: 'contextdb',
      type: 'deploy.completed',
      severity: 'info',
      state: 'resolved',
      title: 'contextdb deployed successfully',
      occurredAt: minutesAgo(35),
    },
    {
      id: 'evt-392',
      source: 'host-runtime',
      app: 'platform',
      type: 'host.recovered',
      severity: 'info',
      state: 'resolved',
      title: 'Host runtime recovery completed',
      occurredAt: minutesAgo(220),
    },
  ],
}

export const activeIncidents = {
  incidents: [
    {
      correlationKey: 'signal-sideband:web:health',
      app: 'signal-sideband',
      latestSeverity: 'critical',
      latestType: 'allocation.unhealthy',
      latestTitle: 'Web allocation is repeatedly restarting',
      eventCount: 3,
      firstSeen: minutesAgo(18),
      lastSeen: minutesAgo(6),
      openCount: 2,
      latestEventId: 'evt-401',
    },
  ],
}

export const operations = {
  count: 1,
  operations: [
    {
      id: 'op_812',
      sagaId: 'saga-812f4c19',
      kind: 'app.deploy',
      app: 'signal-sideband',
      status: 'running',
      attempts: 1,
      maxAttempts: 3,
      risk: 'mutable',
      message: 'Waiting for Nomad health gate',
      startedAt: minutesAgo(4),
      updatedAt: minutesAgo(1),
    },
  ],
}

export const capabilities = {
  protocolVersion: 1,
  serverVersion: 'v2.20.0-control-3-g067fc8d',
  features: [
    'fleet-v1',
    'fleet-inventory',
    'durable-fleet-capacity-plans',
    'fleet-reconciliation-v1',
    'fleet-github-app-v1',
    'durable-app-recovery-v1',
    'app-creation',
    'host-metrics',
  ],
}

export const appSnapshots = [
  {
    filename: 'signal_sideband_pre-migrate_20260825T190415.dump',
    database: 'signal_sideband',
    commitSha: 'pre-migrate',
    timestamp: '20260825T190415',
    createdAt: '2026-08-25T19:04:15Z',
    size: 18_874_368,
  },
  {
    filename: 'signal_sideband_1c1a9c1d4b6_20260825T163044.dump',
    database: 'signal_sideband',
    commitSha: '1c1a9c1d4b6',
    timestamp: '20260825T163044',
    createdAt: '2026-08-25T16:30:44Z',
    size: 18_350_080,
  },
  {
    filename: 'signal_sideband_manual_20260824T221206.dump',
    database: 'signal_sideband',
    commitSha: 'manual',
    timestamp: '20260824T221206',
    createdAt: '2026-08-24T22:12:06Z',
    size: 17_825_792,
  },
  {
    filename: 'signal_sideband_c6134a30b216_20260823T142105.dump',
    database: 'signal_sideband',
    commitSha: 'c6134a30b216',
    timestamp: '20260823T142105',
    createdAt: '2026-08-23T14:21:05Z',
    size: 17_301_504,
  },
]

export const fleetInventory = {
  schemaVersion: 'norn.fleet-inventory/v1',
  configured: true,
  source: 'cluster.yaml',
  digest: 'sha256:6df8c9be820734f424101e89b49f7cffabfe902c81a4c6f4bf2b2038f714ce81',
  document: {
    apiVersion: 'norn.dev/fleet/v1',
    kind: 'Cluster',
    metadata: {
      environment: 'production',
      repository: 'antiartificial/norn-fleet',
      workflowUrl: 'https://github.com/antiartificial/norn-fleet/actions/workflows/apply.yml',
    },
    cluster: { name: 'production-nyc3', provider: 'digitalocean', region: 'nyc3' },
  },
  validation: {
    schemaVersion: 'norn.validation-report/v1',
    documentKind: 'fleet',
    name: 'production-nyc3',
    valid: true,
    findings: [],
  },
  nodePools: {
    app: {
      size: 's-4vcpu-8gb', min: 2, desired: 3, max: 8,
      labels: { workload: 'application services' },
      replacement: { strategy: 'blueGreen', requireCapacityHeadroom: true, drainTimeout: '20m', requireReadiness: true },
    },
    edge: {
      size: 's-2vcpu-4gb', min: 2, desired: 2, max: 4,
      labels: { workload: 'regional ingress' },
      replacement: { strategy: 'rolling', requireCapacityHeadroom: true, drainTimeout: '10m', requireReadiness: true },
    },
  },
}

export const fleetPlans = {
  count: 1,
  plans: [
    {
      id: 'op_fleet_9a21',
      kind: 'fleet.capacity-plan',
      status: 'succeeded',
      createdAt: minutesAgo(38),
      updatedAt: minutesAgo(12),
      payload: {
        pool: 'app',
        action: 'scale',
        current: { desired: 2, size: 's-4vcpu-8gb' },
        proposed: { desired: 3, size: 's-4vcpu-8gb' },
        workflowUrl: 'https://github.com/antiartificial/norn-fleet/actions/workflows/apply.yml',
      },
    },
  ],
}

export const fleetReconciliations = {
  schemaVersion: 'norn.fleet-reconciliations/v1',
  planId: 'op_fleet_9a21',
  count: 5,
  reconciliations: [
    ['infrastructure_applied', 30],
    ['inventory_generated', 25],
    ['nodes_configured', 20],
    ['nodes_enrolled', 16],
    ['readiness_verified', 12],
  ].map(([phase, age], index) => ({
    id: `op_fleet_9a21_checkpoint_${index + 1}`,
    kind: 'fleet.reconciliation',
    status: 'succeeded',
    updatedAt: minutesAgo(age),
    payload: { phase },
  })),
}

export const fleetGitHubStatus = {
  schemaVersion: 'norn.fleet-github-status/v1',
  configured: true,
  connected: true,
  repository: 'antiartificial/norn-fleet',
  installationId: 42117,
  defaultBranch: 'main',
  configPath: 'environments/production/nyc3/cluster.yaml',
  planWorkflow: 'plan.yml',
  applyWorkflow: 'apply.yml',
}

export const serviceManifest = {
  version: 1,
  generatedAt: minutesAgo(1),
  networkMode: 'consul-connect',
  services: [
    {
      name: 'signal-sideband-web',
      app: 'signal-sideband',
      process: 'web',
      type: 'http',
      status: 'degraded',
      healthPath: '/healthz',
      reachability: { endpointScope: 'public', instanceScope: 'cluster', exposure: 'external', routable: true },
      endpoints: [{ url: 'https://sideband.example.com' }],
      instances: [{ node: 'mini', address: '192.0.2.40', port: 8080, status: 'running' }],
    },
    {
      name: 'signal-sideband-media-worker',
      app: 'signal-sideband',
      process: 'media-worker',
      type: 'worker',
      status: 'running',
      reachability: { endpointScope: 'none', instanceScope: 'cluster', exposure: 'internal', routable: false },
    },
    {
      name: 'contextdb-web',
      app: 'contextdb',
      process: 'web',
      type: 'http',
      status: 'running',
      reachability: { endpointScope: 'public', instanceScope: 'cluster', exposure: 'external', routable: true },
      endpoints: [{ url: 'https://context.example.com' }],
    },
  ],
}

export const accessPatterns = {
  windowHours: 168,
  idleAfterHours: 72,
  patterns: [
    {
      app: 'field-harbor-digest',
      process: 'digest',
      type: 'cron',
      status: 'quiet',
      windowHours: 168,
      totalRequests: 7,
      successes: 7,
      clientErrors: 0,
      serverErrors: 0,
      lastSeen: minutesAgo(72 * 60),
      quietForHours: 72,
      activeHours: 1,
      hourlyUtc: { '7': 7 },
      weekdayUtc: { '1': 1, '2': 1, '3': 1, '4': 1, '5': 1, '6': 1, '0': 1 },
      idleCandidate: true,
      idleReason: 'No interactive traffic in the last 72 hours.',
      recommendedAction: 'Keep the scheduled process; no always-on allocation is needed.',
      confidence: 'high',
    },
  ],
}

export const ingress = { hostnames: ['sideband.example.com', 'context.example.com'] }

export const cronHistory = [
  {
    process: 'digest',
    schedule: '0 7 * * *',
    paused: false,
    runs: [
      { jobId: 'periodic-field-harbor-digest/441', status: 'dead', startedAt: minutesAgo(64) },
      { jobId: 'periodic-field-harbor-digest/440', status: 'dead', startedAt: minutesAgo(1500) },
    ],
  },
]

export const functionExecutions = [
  {
    id: 'invoke-88c012ab',
    app: 'archive-thumb',
    process: 'render',
    status: 'complete',
    exitCode: 0,
    startedAt: minutesAgo(22),
    finishedAt: minutesAgo(22),
    durationMs: 2100,
  },
  {
    id: 'invoke-87da0921',
    app: 'archive-thumb',
    process: 'render',
    status: 'complete',
    exitCode: 0,
    startedAt: minutesAgo(48),
    finishedAt: minutesAgo(48),
    durationMs: 4800,
  },
]

export const stats = {
  totalBuilds: 14,
  totalDeploys: 6,
  totalFailures: 1,
  services: 9,
  containers: 11,
  mostPopularApp: 'signal-sideband',
  mostPopularN: 5,
  longestPod: 'contextdb.web[0]',
  longestApp: 'contextdb',
  longestDuration: '36m',
}

export const logs = [
  '2026-08-02T21:17:02Z signal-sideband web[1] starting application server',
  '2026-08-02T21:17:04Z connected to postgres signal_sideband',
  '2026-08-02T21:17:12Z WARN websocket upstream closed unexpectedly',
  '2026-08-02T21:17:13Z allocation restart count=5 window=5m',
  '2026-08-02T21:17:15Z healthz failed: registration cache locked',
  '2026-08-02T21:18:01Z Norn queued app.deploy ref=8f42ac1 drain=wait',
].join('\n')

export function cliOutput(name) {
  switch (name) {
    case 'status':
      return `NORN apps\n\n● signal-sideband     unhealthy  2/3  1c1a9c1  update available  sideband.example.com\n● contextdb           healthy    2/2  b7fb419  core              context.example.com\n● field-harbor-digest healthy    cron de71b90  next 7:00 AM\n● archive-thumb       healthy    func c0ffee1  on demand\n\n4 apps discovered · 9 services · 11 containers`
    case 'operations':
      return `operations\n\nID        KIND          APP              STATUS     REF       AGE\nop_812    app.deploy    signal-sideband  running    8f42ac1   4m\nop_811    app.preflight signal-sideband  succeeded  8f42ac1   7m\nop_810    app.deploy    signal-sideband  failed     1c1a9c1   83m\n\nactive operations: 1`
    case 'platform':
      return `norn platform operations\n\nhealth       ok\nrelease      v2.20.0-control-3-g067fc8d current\nservices     9 discovered, 11 containers\noperations   1 active, drain mode wait\nobservability bundle available, retention 30d / 8GB\nsecrets      0 plaintext migration items\nbeacon       1 warning, 0 critical in last 24h`
    case 'proxy-plan':
      return `proxy cutover plan\n\ncurrent API    127.0.0.1:8800\ncandidate API  127.0.0.1:18802\nmode           switch upstream after candidate postflight\nrollback       switch upstream back to previous port\n\nNo Nomad, Consul, Postgres, or app allocation restart required.`
    case 'endpoints':
      return `signal-sideband endpoints\n\nEXTERNAL  sideband.example.com               enabled  cloudflared\nINTERNAL  signal-sideband.service.consul     ready    consul\n\ncloudflared rule: present\nDNS route:        present`
    case 'fleet':
      return `POOL  SIZE           MIN  DESIRED  MAX  STRATEGY   DRAIN\napp   s-4vcpu-8gb     2    3        8    blueGreen  20m\nedge  s-2vcpu-4gb     2    2        4    rolling    10m`
    case 'snapshots':
      return `snapshots for signal-sideband\n\n  TIMESTAMP        CREATED               COMMIT        DATABASE         SIZE     FILE\n  20260825T190415  2026-08-25T19:04:15Z  pre-migrate   signal_sideband  18.0 MB  signal_sideband_pre-migrate_20260825T190415.dump\n  20260825T163044  2026-08-25T16:30:44Z  1c1a9c1d4b6  signal_sideband  17.5 MB  signal_sideband_1c1a9c1d4b6_20260825T163044.dump\n  20260824T221206  2026-08-24T22:12:06Z  manual        signal_sideband  17.0 MB  signal_sideband_manual_20260824T221206.dump\n  20260823T142105  2026-08-23T14:21:05Z  c6134a30b216  signal_sideband  16.5 MB  signal_sideband_c6134a30b216_20260823T142105.dump`
    default:
      return ''
  }
}
