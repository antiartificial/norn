import DefaultTheme from 'vitepress/theme'
import type { Theme } from 'vitepress'
import PipelineSimulator from './components/PipelineSimulator.vue'
import InfraspecBuilder from './components/InfraspecBuilder.vue'
import HealthChecker from './components/HealthChecker.vue'

export default {
  extends: DefaultTheme,
  enhanceApp({ app }) {
    app.component('PipelineSimulator', PipelineSimulator)
    app.component('InfraspecBuilder', InfraspecBuilder)
    app.component('HealthChecker', HealthChecker)
  },
} satisfies Theme
