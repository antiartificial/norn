export interface RepoSpec {
  url: string
  branch?: string
  webhookSecret?: string
  autoDeploy?: boolean
  repoWeb?: string
}

export interface VolumeSpec {
  name: string
  mountPath: string
  size?: string
  hostPath?: string
}

export interface InfraSpec {
  app: string
  role: 'webserver' | 'worker' | 'cron' | 'function'
  core?: boolean
  deploy?: boolean
  port?: number
  healthcheck?: string
  hosts?: {
    external?: string
    internal?: string
  }
  build?: {
    dockerfile: string
    test?: string
  }
  services?: {
    postgres?: { database: string; migrations?: string }
    kv?: { namespace: string }
    events?: { topics: string[] }
    storage?: { bucket: string; provider?: string }
  }
  secrets?: string[]
  migrations?: { command: string; database: string }
  replicas?: number
  artifacts?: { retain: number }
  repo?: RepoSpec
  volumes?: VolumeSpec[]
  alerts?: AlertConfig
  schedule?: string
  command?: string
  runtime?: string
  timeout?: number
  function?: {
    timeout?: number
    trigger?: string
    memory?: string
  }
}

export interface PodInfo {
  name: string
  status: string
  ready: boolean
  restarts: number
  startedAt: string
}

export type ForgeStatus = 'unforged' | 'forging' | 'forged' | 'forge_failed' | 'tearing_down'

export interface ForgeResources {
  deploymentName?: string
  deploymentNs?: string
  serviceName?: string
  serviceNs?: string
  externalHost?: string
  internalHost?: string
  cloudflaredRule?: boolean
  dnsRoute?: boolean
}

export interface ForgeState {
  app: string
  status: ForgeStatus
  steps: StepLog[]
  resources: ForgeResources
  error?: string
  startedAt?: string
  finishedAt?: string
}

export interface AppStatus {
  spec: InfraSpec
  healthy: boolean
  ready: string
  commitSha: string
  deployedAt: string
  pods: PodInfo[]
  forgeState?: ForgeState
  cronState?: CronState
  remoteHeadSha?: string
}

export interface StepLog {
  step: string
  status: string
  durationMs?: number
  output?: string
  startedAt?: number
}

export interface Deployment {
  id: string
  app: string
  commitSha: string
  imageTag: string
  status: string
  steps: StepLog[]
  error?: string
  startedAt: string
  finishedAt?: string
}

export interface HealthCheck {
  id: string
  app: string
  healthy: boolean
  responseMs: number
  checkedAt: string
}

export interface AlertConfig {
  window: string
  threshold: number
}

export interface Artifact {
  imageTag: string
  commitSha: string
  status: string
  deployedAt: string
}

export interface Snapshot {
  filename: string
  database: string
  commitSha: string
  timestamp: string
  sizeBytes: number
}

export interface CronExecution {
  id: number
  app: string
  imageTag: string
  status: 'running' | 'succeeded' | 'failed' | 'timed_out'
  exitCode: number
  output: string
  durationMs: number
  startedAt: string
  finishedAt?: string
}

export interface CronState {
  app: string
  schedule: string
  paused: boolean
  nextRunAt?: string
}

export interface FuncExecution {
  id: number
  app: string
  imageTag: string
  status: 'running' | 'succeeded' | 'failed' | 'timed_out'
  exitCode: number
  output: string
  durationMs: number
  startedAt: string
  finishedAt?: string
}

export interface WSEvent {
  type: string
  appId: string
  payload: unknown
}
