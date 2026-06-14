export interface RepoSpec {
  url: string
  branch?: string
  autoDeploy?: boolean
  repoWeb?: string
}

export interface Process {
  port?: number
  command?: string
  schedule?: string
  function?: {
    timeout?: string
    memory?: number
  }
  health?: {
    path: string
    interval?: string
    timeout?: string
  }
  metrics?: {
    enabled?: boolean
    path?: string
    port?: number
  }
  scaling?: {
    min?: number
    max?: number
    perRegion?: number
  }
  resources?: {
    cpu?: number
    memory?: number
  }
}

export interface Endpoint {
  url: string
  region?: string
}

export interface InfraSpec {
  name: string
  deploy?: boolean
  processes: Record<string, Process>
  services?: string[]
  secrets?: string[]
  migrations?: string
  env?: Record<string, string>
  repo?: RepoSpec
  build?: {
    dockerfile?: string
    test?: string
  }
  infrastructure?: {
    kafka?: { topics?: string[] }
    postgres?: { database: string }
    redis?: { namespace?: string }
    nats?: { streams?: string[] }
    objectStorage?: {
      provider?: string
      buckets?: Array<{
        name: string
        access?: string
        public?: boolean
        prefix?: string
        env?: string
      }>
    }
  }
  endpoints?: Endpoint[]
}

export interface Allocation {
  id: string
  taskGroup: string
  status: string
  healthy?: boolean
  nodeId?: string
  nodeAddress?: string
  nodeName?: string
  nodeProvider?: string // local, do, hz, remote
  nodeRegion?: string
  startedAt?: string
}

export interface AppStatus {
  spec: InfraSpec
  nomadStatus: string
  healthy: boolean
  allocations: Allocation[]
}

export interface SagaEvent {
  id: string
  sagaId: string
  timestamp: string
  source: string
  app: string
  category: string
  action: string
  message: string
  metadata?: Record<string, string>
}

export interface Deployment {
  id: string
  app: string
  commitSha: string
  imageTag: string
  sagaId: string
  status: string
  startedAt: string
  finishedAt?: string
}

export interface WSEvent {
  type: string
  appId: string
  payload: unknown
}

export interface NotificationChannel {
  id: string
  provider: string
  name: string
  url: string
  token?: string
  userKey?: string
  severities?: string[]
  createdAt: string
}

export interface DeployGroup {
  name: string
  apps: Array<{
    app: string
    waitReady?: boolean
  }>
}

export interface RemoteSnapshot {
  key: string
  size: number
  lastModified: string
}

export interface CanaryStatus {
  id?: string
  jobId?: string
  status: string
  statusDescription?: string
  isCanary?: boolean
}
