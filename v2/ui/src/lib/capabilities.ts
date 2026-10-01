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

export function supportsReleasePipeline(capabilities?: CapabilitiesResponse): boolean {
  return capabilities?.authority !== 'fleet-only'
    && releasePipelineFeatures.every((feature) => capabilities?.features.includes(feature))
    && releasePipelineEndpoints.every((endpoint) => Boolean(capabilities?.endpoints?.[endpoint]))
}
