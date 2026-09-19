// Client-side DigitalOcean size catalog + platform policy for the Fleet Builder.
//
// Design-time stand-in for a future server /api/v1/fleet/plan endpoint (Phase C). Keep these
// values in lock-step with the macOS client (NornUI/Features/FleetBuilder/FleetCatalog.swift)
// so both platforms validate and cost a fleet identically.

export interface FleetSize {
  slug: string
  vcpu: number
  memGB: number
  usdMonthly: number
}

export const NODE_SIZES: FleetSize[] = [
  { slug: 's-2vcpu-4gb', vcpu: 2, memGB: 4, usdMonthly: 24 },
  { slug: 's-4vcpu-8gb', vcpu: 4, memGB: 8, usdMonthly: 48 },
  { slug: 'g-2vcpu-8gb', vcpu: 2, memGB: 8, usdMonthly: 63 },
  { slug: 'g-4vcpu-16gb', vcpu: 4, memGB: 16, usdMonthly: 126 },
  { slug: 's-8vcpu-16gb', vcpu: 8, memGB: 16, usdMonthly: 96 },
]

export const MANAGED_SIZES: FleetSize[] = [
  { slug: 'db-s-1vcpu-2gb', vcpu: 1, memGB: 2, usdMonthly: 15 },
  { slug: 'db-s-2vcpu-4gb', vcpu: 2, memGB: 4, usdMonthly: 60 },
  { slug: 'db-s-4vcpu-8gb', vcpu: 4, memGB: 8, usdMonthly: 120 },
  { slug: 'db-s-6vcpu-16gb', vcpu: 6, memGB: 16, usdMonthly: 240 },
  { slug: 'db-s-8vcpu-32gb', vcpu: 8, memGB: 32, usdMonthly: 480 },
  { slug: 'db-s-16vcpu-64gb', vcpu: 16, memGB: 64, usdMonthly: 960 },
]

export const SPACES_REGIONS = ['nyc3', 'sfo3', 'ams3', 'sgp1', 'fra1', 'syd1', 'blr1']
export const REGION_OPTIONS = ['nyc3', 'sfo3', 'fra1', 'tor1']
export const SECOND_REGION_DEFAULT = 'sfo3'

export const LB_MONTHLY = 12
export const SPACES_MONTHLY = 5

export const CONTROL_MIN = { vcpu: 2, memGB: 4 }
export const APP_MIN = { vcpu: 1, memGB: 2 }
export const DB_SELF_MIN = { vcpu: 2, memGB: 8 }

export const nodeSize = (slug: string): FleetSize | undefined => NODE_SIZES.find(s => s.slug === slug)
export const managedSize = (slug: string): FleetSize | undefined => MANAGED_SIZES.find(s => s.slug === slug)
export const anySize = (slug: string): FleetSize | undefined => nodeSize(slug) ?? managedSize(slug)
export const hasSpaces = (region: string): boolean => SPACES_REGIONS.includes(region)
export const sizeSpec = (size: FleetSize): string => `${size.vcpu}vcpu / ${size.memGB}gb`
