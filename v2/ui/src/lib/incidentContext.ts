import type { Deployment } from '../types/index.ts'

export interface IncidentDeployContext {
  before?: Deployment
  during?: Deployment
}

export function correlateDeploysToIncident(deployments: Deployment[], app: string, incidentStartIso: string): IncidentDeployContext {
  const incidentStart = new Date(incidentStartIso).getTime()
  const relevant = deployments
    .filter(deployment => deployment.app === app && deployment.finishedAt)
    .sort((a, b) => new Date(b.finishedAt ?? 0).getTime() - new Date(a.finishedAt ?? 0).getTime())

  return {
    before: relevant.find(deployment => new Date(deployment.finishedAt ?? 0).getTime() < incidentStart),
    during: relevant.find(deployment => new Date(deployment.finishedAt ?? 0).getTime() >= incidentStart),
  }
}
