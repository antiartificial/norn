import { defineConfig } from 'vitepress'
import { withMermaid } from 'vitepress-plugin-mermaid'

export default withMermaid(
  defineConfig({
    title: 'Norn Docs',
    description: 'Control plane for self-hosted infrastructure',
    base: '/norn/',
    head: [
      ['link', { rel: 'icon', type: 'image/svg+xml', href: '/norn/favicon.svg' }],
    ],
    themeConfig: {
      nav: [
        { text: 'Guide', link: '/v2/guide/getting-started' },
        { text: 'Architecture', link: '/v2/architecture/overview' },
        { text: 'CLI', link: '/v2/cli/' },
        { text: 'Operations', link: '/v2/operations/deploying' },
        {
          text: 'v2',
          items: [
            { text: 'v2 (latest)', link: '/v2/guide/getting-started' },
            { text: 'v1', link: '/v1/guide/getting-started' },
          ],
        },
      ],
      sidebar: {
        '/v2/': [
          {
            text: 'Guide',
            items: [
              { text: 'Getting Started', link: '/v2/guide/getting-started' },
              { text: 'Concepts', link: '/v2/guide/concepts' },
              { text: 'Infraspec Reference', link: '/v2/guide/infraspec-reference' },
              { text: 'Functions', link: '/v2/guide/functions' },
            ],
          },
          {
            text: 'Architecture',
            items: [
              { text: 'Overview', link: '/v2/architecture/overview' },
              { text: 'Deploy Pipeline', link: '/v2/architecture/deploy-pipeline' },
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
              { text: 'Secrets', link: '/v2/infrastructure/secrets' },
              { text: 'Volumes', link: '/v2/infrastructure/volumes' },
              { text: 'Cloudflare', link: '/v2/infrastructure/cloudflare' },
            ],
          },
          {
            text: 'Operations',
            items: [
              { text: 'Deploying', link: '/v2/operations/deploying' },
              { text: 'Cron Jobs', link: '/v2/operations/cron' },
              { text: 'Snapshots', link: '/v2/operations/snapshots' },
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
        pattern: 'https://github.com/antiartificial/norn/edit/main/docs/:path',
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
