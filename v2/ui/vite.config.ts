import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { configDefaults } from 'vitest/config'

const apiTarget = process.env.NORN_API ?? 'http://127.0.0.1:8800'
const wsTarget = apiTarget.replace(/^http/, 'ws')

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': apiTarget,
      '/ws': {
        target: wsTarget,
        ws: true,
      },
    },
  },
  test: {
    // The browser fixture uses node:test and has its own package script.
    exclude: [...configDefaults.exclude, 'browser-fixture/**'],
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
  },
})
