export interface RepoSpec {
  url: string
  branch?: string
  webhookSecret?: string
  autoDeploy?: boolean
}

export interface VolumeSpec {
  name: string
  mountPath: string
  size: string
}

export interface InfraSpec {
  app: string
  role: 'webserver' | 'worker' | 'cron'
  core?: boolean
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
  }
  secrets?: string[]
  migrations?: { command: string; database: string }
  artifacts?: { retain: number }
  repo?: RepoSpec
  volumes?: VolumeSpec[]
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
}

export interface StepLog {
  step: string
  status: string
  durationMs?: number
  output?: string
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

export interface WSEvent {
  type: string
  appId: string
  payload: unknown
}
