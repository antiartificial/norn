import type { Deployment } from '../types/index.ts'

export interface DeploymentRunGroup {
  key: string
  app: string
  latest: Deployment
  earlier: Deployment[]
}

export function groupConsecutiveDeployments(deployments: Deployment[]): DeploymentRunGroup[] {
  const groups: DeploymentRunGroup[] = []

  for (const deploy of deployments) {
    const current = groups[groups.length - 1]
    if (current?.app === deploy.app) {
      current.earlier.push(deploy)
      continue
    }
    groups.push({
      key: deploy.id,
      app: deploy.app,
      latest: deploy,
      earlier: [],
    })
  }

  return groups
}
