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
        { text: 'Guide', link: '/guide/getting-started' },
        { text: 'Architecture', link: '/architecture/overview' },
        { text: 'CLI', link: '/cli/' },
        { text: 'Operations', link: '/operations/deploying' },
      ],
      sidebar: [
        {
          text: 'Guide',
          items: [
            { text: 'Getting Started', link: '/guide/getting-started' },
            { text: 'Concepts', link: '/guide/concepts' },
            { text: 'Infraspec Reference', link: '/guide/infraspec-reference' },
            { text: 'Hello World', link: '/guide/hello-world' },
            { text: 'Roles', link: '/guide/roles' },
            { text: 'Functions', link: '/guide/functions' },
            { text: 'Object Storage', link: '/guide/object-storage' },
          ],
        },
        {
          text: 'Architecture',
          items: [
            { text: 'Overview', link: '/architecture/overview' },
            { text: 'Deploy Pipeline', link: '/architecture/deploy-pipeline' },
            { text: 'Forge Pipeline', link: '/architecture/forge-pipeline' },
            { text: 'Data Model', link: '/architecture/data-model' },
            { text: 'WebSocket', link: '/architecture/websocket' },
          ],
        },
        {
          text: 'CLI',
          items: [
            { text: 'Installation', link: '/cli/' },
            { text: 'Commands', link: '/cli/commands' },
            { text: 'Cluster Management', link: '/cli/cluster' },
          ],
        },
        {
          text: 'Infrastructure',
          items: [
            { text: 'Kubernetes', link: '/infrastructure/kubernetes' },
            { text: 'Shared Services', link: '/infrastructure/shared-services' },
            { text: 'Secrets', link: '/infrastructure/secrets' },
            { text: 'Terraform', link: '/infrastructure/terraform' },
            { text: 'Cloudflare', link: '/infrastructure/cloudflare' },
            { text: 'Cloud Providers', link: '/infrastructure/cloud-providers' },
          ],
        },
        {
          text: 'Operations',
          items: [
            { text: 'Deploying', link: '/operations/deploying' },
            { text: 'Rollback', link: '/operations/rollback' },
            { text: 'Troubleshooting', link: '/operations/troubleshooting' },
            { text: 'Makefile', link: '/operations/makefile' },
          ],
        },
      ],
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
