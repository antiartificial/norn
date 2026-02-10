export interface InfraSpec {
  app: string
  role: 'webserver' | 'worker' | 'cron'
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
}

export interface PodInfo {
  name: string
  status: string
  ready: boolean
  restarts: number
  startedAt: string
}

export interface AppStatus {
  spec: InfraSpec
  healthy: boolean
  ready: string
  commitSha: string
  deployedAt: string
  pods: PodInfo[]
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
