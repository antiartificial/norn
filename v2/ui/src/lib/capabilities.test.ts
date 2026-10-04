import { describe, expect, it } from 'vitest'
import signedStagingCapabilitiesJSON from '../../../api/testdata/client-contract/v1/signed-staging-capabilities.json?raw'
import type { CapabilitiesResponse } from '../types/index.ts'
import { releasePipelineEndpointPaths, releasePipelineEndpoints, releasePipelineFeatures, supportsReleasePipeline } from './capabilities.ts'

const signedStagingCapabilities = JSON.parse(signedStagingCapabilitiesJSON) as CapabilitiesResponse

describe('supportsReleasePipeline', () => {
  it('accepts the Norn-owned signed staging capability contract', () => {
    expect(supportsReleasePipeline(signedStagingCapabilities)).toBe(true)
  })

  it.each(releasePipelineEndpoints)('requires the %s endpoint', (missingEndpoint) => {
    const endpoints = { ...signedStagingCapabilities.endpoints }
    delete endpoints[missingEndpoint]

    expect(supportsReleasePipeline({ ...signedStagingCapabilities, endpoints })).toBe(false)
  })

  it.each(releasePipelineEndpoints)('requires the supported path for %s', (endpoint) => {
    expect(supportsReleasePipeline({
      ...signedStagingCapabilities,
      endpoints: { ...signedStagingCapabilities.endpoints, [endpoint]: `${releasePipelineEndpointPaths[endpoint]}/unexpected` },
    })).toBe(false)
  })

  it.each(releasePipelineFeatures)('requires the %s feature', (missingFeature) => {
    const features = signedStagingCapabilities.features.filter((feature) => feature !== missingFeature)
    expect(supportsReleasePipeline({ ...signedStagingCapabilities, features })).toBe(false)
  })

  it('does not expose release controls on a fleet-only authority', () => {
    expect(supportsReleasePipeline({ ...signedStagingCapabilities, authority: 'fleet-only' })).toBe(false)
  })

  it.each([
    ['missing features', { ...signedStagingCapabilities, features: undefined }],
    ['null features', { ...signedStagingCapabilities, features: null }],
    ['non-array features', { ...signedStagingCapabilities, features: 'release-provenance-v1' }],
    ['missing endpoints', { ...signedStagingCapabilities, endpoints: undefined }],
    ['null endpoints', { ...signedStagingCapabilities, endpoints: null }],
    ['array endpoints', { ...signedStagingCapabilities, endpoints: [] }],
    ['non-object endpoints', { ...signedStagingCapabilities, endpoints: 'releaseQualifications' }],
    ['truthy endpoint values', { ...signedStagingCapabilities, endpoints: { ...signedStagingCapabilities.endpoints, releaseQualifications: true } }],
    ['unsupported protocol version', { ...signedStagingCapabilities, protocolVersion: 2 }],
  ])('fails closed for %s', (_name, malformed) => {
    expect(supportsReleasePipeline(malformed as CapabilitiesResponse)).toBe(false)
  })
})
