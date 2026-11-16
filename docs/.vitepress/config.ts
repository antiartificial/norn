import { defineConfig } from 'vitepress'
import { withMermaid } from 'vitepress-plugin-mermaid'

export default withMermaid(
  defineConfig({
    title: 'Norn 3.0.0',
    description: 'Fleet architecture, online platform upgrades, and recoverable operations',
    base: '/norn/',
    head: [
      ['link', { rel: 'icon', type: 'image/svg+xml', href: '/norn/favicon.svg' }],
    ],
    themeConfig: {
      nav: [
        { text: 'V3 guide', link: '/v3/guide/getting-started' },
        { text: 'Architecture', link: '/v3/architecture/overview' },
        { text: 'Release status', link: '/v3/release-program-milestones' },
        {
          text: 'Versions',
          items: [
            { text: 'V3 · Norn 3.0.0', link: '/v3/' },
            { text: 'V2 documentation', link: '/v2/' },
            { text: 'V1 documentation', link: '/v1/guide/getting-started' },
          ],
        },
      ],
      sidebar: {
        '/v3/': [
          {
            text: 'Norn 3.0.0',
            items: [
              { text: 'Overview', link: '/v3/' },
              { text: 'Start with V3', link: '/v3/guide/getting-started' },
              { text: 'Capability status', link: '/v3/reference/capabilities' },
              { text: 'Version identity', link: '/v3/reference/versioning' },
            ],
          },
          {
            text: 'Architecture',
            items: [
              { text: 'Runtime and authority', link: '/v3/architecture/overview' },
              { text: 'Repository boundaries', link: '/v3/architecture/repository-boundaries' },
            ],
          },
          {
            text: 'Operations',
            items: [
              { text: 'Online platform upgrades', link: '/v3/operations/online-upgrades' },
              { text: 'Recovery and qualification', link: '/v3/operations/recovery' },
              { text: 'Launch state', link: '/v3/LAUNCH-STATE' },
              { text: 'Release milestones', link: '/v3/release-program-milestones' },
            ],
          },
          {
            text: 'Design history',
            collapsed: true,
            items: [
              { text: 'Planning records', link: '/v3/README' },
              { text: 'Decision records', link: '/v3/architecture-roadmap' },
              { text: 'Launch history', link: '/v3/LAUNCH-HISTORY' },
            ],
          },
        ],
        '/v2/': [
          {
            text: 'Guide',
            items: [
              { text: 'Why Norn', link: '/v2/guide/why-norn' },
              { text: 'Getting Started', link: '/v2/guide/getting-started' },
              { text: 'Release Recap', link: '/v2/guide/release-recap' },
              { text: 'Concepts', link: '/v2/guide/concepts' },
              { text: 'Infraspec Reference', link: '/v2/guide/infraspec-reference' },
              { text: 'Functions', link: '/v2/guide/functions' },
              { text: 'Next Steps', link: '/v2/guide/next-steps' },
            ],
          },
          {
            text: 'Architecture',
            items: [
              { text: 'Overview', link: '/v2/architecture/overview' },
              { text: 'Native Control Protocol', link: '/v2/architecture/control-protocol' },
              { text: 'Deploy Pipeline', link: '/v2/architecture/deploy-pipeline' },
              { text: 'Platform Upgrades', link: '/v2/architecture/platform-upgrades' },
              { text: 'Nomad Translator', link: '/v2/architecture/nomad-translator' },
              { text: 'Saga Events', link: '/v2/architecture/saga-events' },
              { text: 'WebSocket', link: '/v2/architecture/websocket' },
            ],
          },
          {
            text: 'CLI',
            items: [
              { text: 'Installation', link: '/v2/cli/' },
              { text: 'Commands', link: '/v2/cli/commands' },
            ],
          },
          {
            text: 'Infrastructure',
            items: [
              { text: 'Nomad & Consul', link: '/v2/infrastructure/nomad-consul' },
              { text: 'Workload Connectors', link: '/v2/infrastructure/workload-connectors' },
              { text: 'Fleet GitOps', link: '/v2/infrastructure/fleet' },
              { text: 'Secrets', link: '/v2/infrastructure/secrets' },
              { text: 'Volumes', link: '/v2/infrastructure/volumes' },
              { text: 'Object Storage', link: '/v2/infrastructure/object-storage' },
              { text: 'Observability', link: '/v2/infrastructure/observability' },
              { text: 'Cloudflare', link: '/v2/infrastructure/cloudflare' },
            ],
          },
          {
            text: 'Operations',
            items: [
              { text: 'Deploying', link: '/v2/operations/deploying' },
              { text: 'Agent Runbook', link: '/v2/operations/agent-runbook' },
              { text: 'Operator Confidence', link: '/v2/operations/operator-confidence' },
              { text: 'Durable Operations', link: '/v2/operations/operations' },
              { text: 'Beacon Events', link: '/v2/operations/beacon' },
              { text: 'Cron Jobs', link: '/v2/operations/cron' },
              { text: 'Snapshots & Recovery', link: '/v2/operations/snapshots' },
              { text: 'Upgrading', link: '/v2/operations/upgrading' },
              { text: 'Host Recovery', link: '/v2/operations/host-recovery' },
              { text: 'Workload Qualification', link: '/v2/operations/workload-qualification' },
              { text: 'Linux HA Lab', link: '/v2/operations/ha-lab' },
              { text: 'Fleet operations', link: '/v2/operations/fleet' },
              { text: 'Fleet staging pilot', link: '/v2/operations/fleet-pilot' },
              { text: 'Durability Pilot', link: '/v2/operations/durability-pilot' },
              { text: 'Production Readiness', link: '/v2/operations/production-readiness' },
              { text: 'Troubleshooting', link: '/v2/operations/troubleshooting' },
            ],
          },
        ],
        '/v1/': [
          {
            text: 'Guide',
            items: [
              { text: 'Getting Started', link: '/v1/guide/getting-started' },
              { text: 'Concepts', link: '/v1/guide/concepts' },
              { text: 'Infraspec Reference', link: '/v1/guide/infraspec-reference' },
              { text: 'Hello World', link: '/v1/guide/hello-world' },
              { text: 'Roles', link: '/v1/guide/roles' },
              { text: 'Functions', link: '/v1/guide/functions' },
              { text: 'Object Storage', link: '/v1/guide/object-storage' },
            ],
          },
          {
            text: 'Architecture',
            items: [
              { text: 'Overview', link: '/v1/architecture/overview' },
              { text: 'Deploy Pipeline', link: '/v1/architecture/deploy-pipeline' },
              { text: 'Forge Pipeline', link: '/v1/architecture/forge-pipeline' },
              { text: 'Data Model', link: '/v1/architecture/data-model' },
              { text: 'WebSocket', link: '/v1/architecture/websocket' },
            ],
          },
          {
            text: 'CLI',
            items: [
              { text: 'Installation', link: '/v1/cli/' },
              { text: 'Commands', link: '/v1/cli/commands' },
              { text: 'Cluster Management', link: '/v1/cli/cluster' },
            ],
          },
          {
            text: 'Infrastructure',
            items: [
              { text: 'Kubernetes', link: '/v1/infrastructure/kubernetes' },
              { text: 'Shared Services', link: '/v1/infrastructure/shared-services' },
              { text: 'Secrets', link: '/v1/infrastructure/secrets' },
              { text: 'Terraform', link: '/v1/infrastructure/terraform' },
              { text: 'Cloudflare', link: '/v1/infrastructure/cloudflare' },
              { text: 'Cloud Providers', link: '/v1/infrastructure/cloud-providers' },
            ],
          },
          {
            text: 'Operations',
            items: [
              { text: 'Deploying', link: '/v1/operations/deploying' },
              { text: 'Rollback', link: '/v1/operations/rollback' },
              { text: 'Troubleshooting', link: '/v1/operations/troubleshooting' },
              { text: 'Makefile', link: '/v1/operations/makefile' },
            ],
          },
        ],
      },
      socialLinks: [
        { icon: 'github', link: 'https://github.com/antiartificial/norn' },
      ],
      search: {
        provider: 'local',
      },
      editLink: {
        pattern: 'https://github.com/antiartificial/norn/edit/master/docs/:path',
        text: 'Edit this page on GitHub',
      },
    },
    ignoreDeadLinks: [
      /^https?:\/\/localhost/,
    ],
    mermaid: {},
    mermaidPlugin: {
      class: 'mermaid',
    },
  })
)
