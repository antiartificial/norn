const now = Date.now()

const minutesAgo = (n) => new Date(now - n * 60_000).toISOString()
const minutesFromNow = (n) => new Date(now + n * 60_000).toISOString()

export const deploySteps = [
  { step: 'clone', status: 'done', durationMs: 4200, output: 'checked out antiartificial/signal-sideband at 8f42ac1' },
  { step: 'build', status: 'done', durationMs: 36100, output: 'docker build completed: signal-sideband:8f42ac1' },
  { step: 'test', status: 'done', durationMs: 9100, output: 'go test ./... ok' },
  { step: 'snapshot', status: 'done', durationMs: 6200, output: 'captured postgres safety snapshot signal_sideband_20260614T1821Z' },
  { step: 'migrate', status: 'done', durationMs: 1800, output: 'no pending migrations' },
  { step: 'deploy', status: 'running', startedAt: now - 24_000, output: 'submitting Nomad job signal-sideband; waiting for allocations to drain' },
]

export const apps = [
  {
    spec: {
      app: 'signal-sideband',
      role: 'webserver',
      deploy: true,
      port: 8080,
      healthcheck: '/healthz',
      replicas: 2,
      repo: {
        url: 'git@github.com:antiartificial/signal-sideband.git',
        branch: 'main',
        autoDeploy: true,
      },
      hosts: {
        external: 'sideband.slopistry.com',
        internal: 'signal-sideband.service.consul',
      },
      build: {
        dockerfile: 'Dockerfile',
        test: 'go test ./...',
      },
      services: {
        postgres: { database: 'signal_sideband' },
        kv: { namespace: 'signal-sideband' },
        events: { topics: ['signal.received', 'signal.replayed'] },
        storage: { bucket: 'signal-sideband-media', provider: 'garage' },
      },
      secrets: ['SIGNAL_NUMBER', 'DATABASE_URL', 'GARAGE_ACCESS_KEY'],
      alerts: { window: '5m', threshold: 3 },
    },
    healthy: false,
    ready: '1/2',
    commitSha: '1c1a9c1d4b6a7c1eaf01b1ba734ea087948b13ee',
    remoteHeadSha: '8f42ac18d8ff7bb26b33a23fd63113360527bca9',
    deployedAt: minutesAgo(83),
    pods: [
      { name: 'signal-sideband.web[0]', status: 'running', ready: true, restarts: 0, startedAt: minutesAgo(84) },
      { name: 'signal-sideband.web[1]', status: 'restarting', ready: false, restarts: 7, startedAt: minutesAgo(6) },
    ],
    forgeState: {
      app: 'signal-sideband',
      status: 'forged',
      steps: [],
      resources: {
        deploymentName: 'signal-sideband',
        serviceName: 'signal-sideband',
        externalHost: 'sideband.slopistry.com',
        internalHost: 'signal-sideband.service.consul',
        cloudflaredRule: true,
        dnsRoute: true,
      },
    },
  },
  {
    spec: {
      app: 'contextdb',
      role: 'webserver',
      core: true,
      deploy: true,
      port: 8787,
      healthcheck: '/health',
      replicas: 1,
      repo: { url: 'git@github.com:antiartificial/contextdb.git', branch: 'main', autoDeploy: true },
      hosts: {
        external: 'contextdb.slopistry.com',
        internal: 'contextdb.service.consul',
      },
      services: {
        postgres: { database: 'contextdb' },
        events: { topics: ['claims.reviewed', 'claims.promoted'] },
      },
      secrets: ['DATABASE_URL', 'OPENAI_API_KEY'],
    },
    healthy: true,
    ready: '2/2',
    commitSha: 'b7fb419b3150de885a62f51464d8152f8af01c2d',
    deployedAt: minutesAgo(36),
    pods: [
      { name: 'contextdb.web[0]', status: 'running', ready: true, restarts: 0, startedAt: minutesAgo(36) },
      { name: 'contextdb.review-worker[0]', status: 'running', ready: true, restarts: 0, startedAt: minutesAgo(36) },
    ],
    forgeState: { app: 'contextdb', status: 'forged', steps: [], resources: {} },
  },
  {
    spec: {
      app: 'field-harbor-digest',
      role: 'cron',
      deploy: true,
      schedule: '0 7 * * *',
      command: './field-harbor digest',
      repo: { url: 'git@github.com:antiartificial/field-harbor.git', branch: 'main', autoDeploy: true },
      services: {
        postgres: { database: 'field_harbor' },
        storage: { bucket: 'field-harbor-archive', provider: 'garage' },
      },
      secrets: ['DATABASE_URL', 'GARAGE_SECRET_KEY'],
    },
    healthy: true,
    ready: 'scheduled',
    commitSha: 'de71b901a99c1deca91f41fe89c4710b0d33d778',
    deployedAt: minutesAgo(140),
    pods: [],
    cronState: {
      app: 'field-harbor-digest',
      schedule: '0 7 * * *',
      paused: false,
      nextRunAt: minutesFromNow(42),
    },
    forgeState: { app: 'field-harbor-digest', status: 'forged', steps: [], resources: {} },
  },
  {
    spec: {
      app: 'archive-thumb',
      role: 'function',
      deploy: true,
      command: './thumb render',
      repo: { url: 'git@github.com:antiartificial/archive-tools.git', branch: 'main' },
      function: { timeout: 600, memory: '1024' },
      services: {
        storage: { bucket: 'archive-renders', provider: 'garage' },
      },
      secrets: ['GARAGE_ACCESS_KEY'],
    },
    healthy: true,
    ready: 'on demand',
    commitSha: 'c0ffee190b68beec5f2ab1c0b78a5f6e641e827a',
    deployedAt: minutesAgo(55),
    pods: [],
    forgeState: { app: 'archive-thumb', status: 'forged', steps: [], resources: {} },
  },
]

export const healthChecks = {
  'signal-sideband': Array.from({ length: 24 }, (_, i) => ({
    id: `sig-${i}`,
    app: 'signal-sideband',
    healthy: i < 15 ? true : i % 3 !== 0,
    responseMs: i < 15 ? 80 + i * 3 : 420 + i * 9,
    checkedAt: minutesAgo(24 - i),
  })),
  contextdb: Array.from({ length: 24 }, (_, i) => ({
    id: `ctx-${i}`,
    app: 'contextdb',
    healthy: true,
    responseMs: 42 + (i % 5) * 4,
    checkedAt: minutesAgo(24 - i),
  })),
}

export const deployments = [
  {
    id: 'dep-signal-sideband-8f42ac1',
    app: 'signal-sideband',
    commitSha: '8f42ac18d8ff7bb26b33a23fd63113360527bca9',
    imageTag: 'signal-sideband:8f42ac1',
    status: 'deploying',
    steps: deploySteps,
    startedAt: minutesAgo(4),
  },
  {
    id: 'dep-signal-sideband-1c1a9c1',
    app: 'signal-sideband',
    commitSha: '1c1a9c1d4b6a7c1eaf01b1ba734ea087948b13ee',
    imageTag: 'signal-sideband:1c1a9c1',
    status: 'failed',
    steps: [
      { step: 'clone', status: 'done', durationMs: 3900 },
      { step: 'build', status: 'done', durationMs: 34400 },
      { step: 'test', status: 'done', durationMs: 8800 },
      { step: 'deploy', status: 'failed', durationMs: 154000, output: 'allocation signal-sideband.web[1] restarted 7 times in 5m' },
    ],
    error: 'health gate failed: one allocation kept restarting',
    startedAt: minutesAgo(83),
    finishedAt: minutesAgo(79),
  },
  {
    id: 'dep-contextdb-b7fb419',
    app: 'contextdb',
    commitSha: 'b7fb419b3150de885a62f51464d8152f8af01c2d',
    imageTag: 'contextdb:b7fb419',
    status: 'deployed',
    steps: [
      { step: 'clone', status: 'done', durationMs: 2500 },
      { step: 'build', status: 'done', durationMs: 18200 },
      { step: 'test', status: 'done', durationMs: 6700 },
      { step: 'snapshot', status: 'done', durationMs: 5100 },
      { step: 'migrate', status: 'done', durationMs: 1200 },
      { step: 'deploy', status: 'done', durationMs: 21800 },
    ],
    startedAt: minutesAgo(36),
    finishedAt: minutesAgo(35),
  },
]

export const cronExecutions = [
  {
    id: 441,
    app: 'field-harbor-digest',
    imageTag: 'field-harbor-digest:de71b90',
    status: 'succeeded',
    exitCode: 0,
    output: 'indexed 312 bookmarks\nuploaded 18 media objects\nsent digest to archive channel',
    durationMs: 32200,
    startedAt: minutesAgo(64),
    finishedAt: minutesAgo(63),
  },
  {
    id: 440,
    app: 'field-harbor-digest',
    imageTag: 'field-harbor-digest:de71b90',
    status: 'succeeded',
    exitCode: 0,
    output: 'no new media; manifest already current',
    durationMs: 7100,
    startedAt: minutesAgo(1500),
    finishedAt: minutesAgo(1499),
  },
]

export const functionExecutions = [
  {
    id: 88,
    app: 'archive-thumb',
    imageTag: 'archive-thumb:c0ffee1',
    status: 'succeeded',
    exitCode: 0,
    output: 'rendered poster thumb for r2://archive-renders/2026/06/sideband.png',
    durationMs: 2100,
    startedAt: minutesAgo(22),
    finishedAt: minutesAgo(22),
  },
  {
    id: 87,
    app: 'archive-thumb',
    imageTag: 'archive-thumb:c0ffee1',
    status: 'succeeded',
    exitCode: 0,
    output: 'rendered 4 gallery thumbnails',
    durationMs: 4800,
    startedAt: minutesAgo(48),
    finishedAt: minutesAgo(48),
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
  '2026-06-14T18:17:02Z signal-sideband web[1] starting signal-cli bridge',
  '2026-06-14T18:17:04Z connected to postgres signal_sideband',
  '2026-06-14T18:17:12Z WARN websocket upstream closed unexpectedly',
  '2026-06-14T18:17:13Z allocation restart count=5 window=5m',
  '2026-06-14T18:17:15Z healthz failed: signal registration cache locked',
  '2026-06-14T18:18:01Z Norn queued app.deploy ref=8f42ac1 drain=wait',
].join('\n')

export function cliOutput(name) {
  switch (name) {
    case 'status':
      return `NORN apps\n\n● signal-sideband   unhealthy  1/2  1c1a9c1  update available  sideband.slopistry.com\n● contextdb         healthy    2/2  b7fb419  core              contextdb.slopistry.com\n● field-harbor-digest healthy  cron de71b90  next 7:00 AM\n● archive-thumb     healthy    func c0ffee1  on demand\n\n4 apps discovered · 9 services · 11 containers`
    case 'operations':
      return `operations\n\nID        KIND          APP              STATUS    REF       AGE\nop_812    app.deploy    signal-sideband  running   8f42ac1   4m\nop_811    app.preflight signal-sideband  done      8f42ac1   7m\nop_810    app.deploy    signal-sideband  failed    1c1a9c1   83m\n\nactive operations: 1`
    case 'platform':
      return `norn platform operations\n\nhealth       ok\nrelease      8f42ac1 current\nservices     9 discovered, 11 containers\noperations   1 active, drain mode wait\nobservability bundle available, retention 30d / 8GB\nsecrets      0 plaintext migration items\nbeacon       1 warning, 0 critical in last 24h`
    case 'proxy-plan':
      return `proxy cutover plan\n\ncurrent API    127.0.0.1:8800\ncandidate API  127.0.0.1:18802\nmode           switch upstream after candidate postflight\nrollback       switch upstream back to previous port\n\nNo Nomad, Consul, Postgres, or app allocation restart required.`
    case 'endpoints':
      return `signal-sideband endpoints\n\nEXTERNAL  sideband.slopistry.com        enabled  cloudflared\nINTERNAL  signal-sideband.service.consul ready    consul\n\ncloudflared rule: present\nDNS route:        present`
    default:
      return ''
  }
}
