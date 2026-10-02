import type { CapabilitiesResponse } from '../types/index.ts'

export const releasePipelineFeatures = [
  'release-provenance-v1',
  'release-qualifications-v2',
  'release-promotions-v1',
] as const

export const releasePipelineEndpoints = [
  'releasePreflight',
  'releaseDeployments',
  'releaseQualifications',
  'releasePromotions',
  'releaseRollbacks',
] as const

export const releasePipelineEndpointPaths: Record<(typeof releasePipelineEndpoints)[number], string> = {
  releasePreflight: '/api/v1/apps/{id}/releases/preflight',
  releaseDeployments: '/api/v1/apps/{id}/releases/deployments',
  releaseQualifications: '/api/v1/apps/{id}/qualifications',
  releasePromotions: '/api/v1/apps/{id}/promotions',
  releaseRollbacks: '/api/v1/apps/{id}/releases/rollbacks',
}

export function supportsReleasePipeline(capabilities?: CapabilitiesResponse): boolean {
  if (!capabilities || typeof capabilities !== 'object' || capabilities.protocolVersion !== 1 || capabilities.authority === 'fleet-only') return false

  const { features, endpoints } = capabilities
  if (!Array.isArray(features) || !endpoints || typeof endpoints !== 'object' || Array.isArray(endpoints)) return false

  return releasePipelineFeatures.every((feature) => features.includes(feature))
    && releasePipelineEndpoints.every((endpoint) => endpoints[endpoint] === releasePipelineEndpointPaths[endpoint])
}
