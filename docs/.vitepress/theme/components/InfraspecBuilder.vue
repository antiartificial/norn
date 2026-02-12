<template>
  <div class="infraspec-builder">
    <div class="builder-layout">
      <div class="form-section">
        <h3>Configuration</h3>

        <div class="form-group">
          <label>App Name</label>
          <input v-model="config.name" type="text" placeholder="my-app" />
        </div>

        <div class="form-group">
          <label>Role</label>
          <select v-model="config.role">
            <option value="webserver">webserver</option>
            <option value="worker">worker</option>
            <option value="cron">cron</option>
          </select>
        </div>

        <div v-if="config.role === 'webserver'" class="form-group">
          <label>Port</label>
          <input v-model.number="config.port" type="number" placeholder="3000" />
        </div>

        <div v-if="config.role === 'webserver'" class="form-group">
          <label>Healthcheck Path</label>
          <input v-model="config.healthcheck" type="text" placeholder="/health" />
        </div>

        <div v-if="config.role === 'cron'" class="form-group">
          <label>Schedule</label>
          <input v-model="config.schedule" type="text" placeholder="*/5 * * * *" />
        </div>

        <div class="form-group">
          <label>External Host (optional)</label>
          <input v-model="config.externalHost" type="text" placeholder="app.example.com" />
        </div>

        <div class="form-group">
          <label>Internal Host (optional)</label>
          <input v-model="config.internalHost" type="text" placeholder="app.internal" />
        </div>

        <div class="form-group">
          <label>Dockerfile Path</label>
          <input v-model="config.dockerfile" type="text" placeholder="Dockerfile" />
        </div>

        <div class="form-group">
          <label>Test Command (optional)</label>
          <input v-model="config.testCommand" type="text" placeholder="npm test" />
        </div>

        <div class="form-group">
          <label>Replicas</label>
          <input v-model.number="config.replicas" type="number" min="1" />
        </div>

        <div class="form-section-title">Services</div>

        <div class="form-group checkbox-group">
          <label class="checkbox-label">
            <input v-model="config.services.postgres.enabled" type="checkbox" />
            PostgreSQL
          </label>
          <input
            v-if="config.services.postgres.enabled"
            v-model="config.services.postgres.database"
            type="text"
            placeholder="Database name"
            class="sub-input"
          />
        </div>

        <div class="form-group checkbox-group">
          <label class="checkbox-label">
            <input v-model="config.services.valkey.enabled" type="checkbox" />
            Valkey
          </label>
          <input
            v-if="config.services.valkey.enabled"
            v-model="config.services.valkey.namespace"
            type="text"
            placeholder="Namespace"
            class="sub-input"
          />
        </div>

        <div class="form-group checkbox-group">
          <label class="checkbox-label">
            <input v-model="config.services.redpanda.enabled" type="checkbox" />
            Redpanda
          </label>
          <input
            v-if="config.services.redpanda.enabled"
            v-model="config.services.redpanda.topics"
            type="text"
            placeholder="topic1,topic2"
            class="sub-input"
          />
        </div>

        <div class="form-group">
          <label>Secrets (comma-separated, optional)</label>
          <input v-model="config.secrets" type="text" placeholder="API_KEY,DB_PASSWORD" />
        </div>
      </div>

      <div class="preview-section">
        <div class="preview-header">
          <h3>infraspec.yaml</h3>
          <button @click="copyToClipboard" class="btn-copy">
            {{ copied ? 'Copied!' : 'Copy' }}
          </button>
        </div>
        <pre class="yaml-preview"><code>{{ generatedYaml }}</code></pre>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed } from 'vue'

const config = ref({
  name: 'my-app',
  role: 'webserver',
  port: 3000,
  healthcheck: '/health',
  schedule: '',
  externalHost: '',
  internalHost: '',
  dockerfile: 'Dockerfile',
  testCommand: '',
  replicas: 1,
  services: {
    postgres: { enabled: false, database: '' },
    valkey: { enabled: false, namespace: '' },
    redpanda: { enabled: false, topics: '' }
  },
  secrets: ''
})

const copied = ref(false)

const generatedYaml = computed(() => {
  const lines = []

  lines.push(`app: ${config.value.name}`)
  lines.push(`role: ${config.value.role}`)

  if (config.value.role === 'webserver') {
    lines.push(`port: ${config.value.port}`)
    if (config.value.healthcheck) {
      lines.push(`healthcheck: ${config.value.healthcheck}`)
    }
  }

  if (config.value.role === 'cron' && config.value.schedule) {
    lines.push(`schedule: "${config.value.schedule}"`)
  }

  if (config.value.externalHost || config.value.internalHost) {
    lines.push('hosts:')
    if (config.value.externalHost) {
      lines.push(`  external: ${config.value.externalHost}`)
    }
    if (config.value.internalHost) {
      lines.push(`  internal: ${config.value.internalHost}`)
    }
  }

  if (config.value.dockerfile || config.value.testCommand) {
    lines.push('build:')
    lines.push(`  dockerfile: ${config.value.dockerfile}`)
    if (config.value.testCommand) {
      lines.push(`  test: ${config.value.testCommand}`)
    }
  }

  if (config.value.replicas > 1) {
    lines.push(`replicas: ${config.value.replicas}`)
  }

  // Services
  const hasServices = config.value.services.postgres.enabled ||
                     config.value.services.valkey.enabled ||
                     config.value.services.redpanda.enabled

  if (hasServices) {
    lines.push('services:')

    if (config.value.services.postgres.enabled) {
      lines.push('  postgres:')
      if (config.value.services.postgres.database) {
        lines.push(`    database: ${config.value.services.postgres.database}`)
      }
    }

    if (config.value.services.valkey.enabled) {
      lines.push('  kv:')
      if (config.value.services.valkey.namespace) {
        lines.push(`    namespace: ${config.value.services.valkey.namespace}`)
      }
    }

    if (config.value.services.redpanda.enabled) {
      lines.push('  events:')
      if (config.value.services.redpanda.topics) {
        const topics = config.value.services.redpanda.topics
          .split(',')
          .map(t => t.trim())
          .filter(t => t)
        if (topics.length > 0) {
          lines.push(`    topics: [${topics.join(', ')}]`)
        }
      }
    }
  }

  // Secrets
  if (config.value.secrets) {
    const secrets = config.value.secrets
      .split(',')
      .map(s => s.trim())
      .filter(s => s)
    if (secrets.length > 0) {
      lines.push('secrets:')
      secrets.forEach(secret => {
        lines.push(`  - ${secret}`)
      })
    }
  }

  return lines.join('\n')
})

const copyToClipboard = async () => {
  try {
    await navigator.clipboard.writeText(generatedYaml.value)
    copied.value = true
    setTimeout(() => {
      copied.value = false
    }, 2000)
  } catch (err) {
    console.error('Failed to copy:', err)
  }
}
</script>

<style scoped>
.infraspec-builder {
  margin: 1rem 0;
  border: 1px solid var(--vp-c-divider);
  border-radius: 8px;
  background: var(--vp-c-bg-soft);
  overflow: hidden;
}

.builder-layout {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0;
}

.form-section,
.preview-section {
  padding: 1.5rem;
}

.form-section {
  border-right: 1px solid var(--vp-c-divider);
  max-height: 600px;
  overflow-y: auto;
}

.preview-section {
  background: var(--vp-c-bg);
}

h3 {
  margin: 0 0 1.5rem 0;
  font-size: 1.1rem;
  color: var(--vp-c-text-1);
}

.form-section-title {
  font-weight: 600;
  margin-top: 1.5rem;
  margin-bottom: 1rem;
  color: var(--vp-c-text-1);
  font-size: 0.95rem;
}

.form-group {
  margin-bottom: 1rem;
}

.form-group label {
  display: block;
  margin-bottom: 0.4rem;
  font-size: 0.85rem;
  font-weight: 500;
  color: var(--vp-c-text-2);
}

.form-group input[type="text"],
.form-group input[type="number"],
.form-group select {
  width: 100%;
  padding: 0.5rem;
  border: 1px solid var(--vp-c-divider);
  border-radius: 4px;
  background: var(--vp-c-bg);
  color: var(--vp-c-text-1);
  font-size: 0.9rem;
  font-family: var(--vp-font-family-mono);
}

.form-group input:focus,
.form-group select:focus {
  outline: none;
  border-color: var(--vp-c-brand);
}

.checkbox-group {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.checkbox-label {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  cursor: pointer;
  font-size: 0.9rem;
}

.checkbox-label input[type="checkbox"] {
  cursor: pointer;
}

.sub-input {
  margin-left: 1.5rem;
  width: calc(100% - 1.5rem);
}

.preview-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 1rem;
}

.btn-copy {
  padding: 0.4rem 0.8rem;
  border: 1px solid var(--vp-c-divider);
  border-radius: 4px;
  background: var(--vp-c-brand);
  color: white;
  cursor: pointer;
  font-size: 0.85rem;
  font-weight: 500;
  transition: all 0.2s;
}

.btn-copy:hover {
  background: var(--vp-c-brand-dark);
}

.yaml-preview {
  margin: 0;
  padding: 1rem;
  background: var(--vp-c-bg-soft);
  border: 1px solid var(--vp-c-divider);
  border-radius: 4px;
  overflow-x: auto;
  max-height: 500px;
  overflow-y: auto;
}

.yaml-preview code {
  font-family: var(--vp-font-family-mono);
  font-size: 0.85rem;
  line-height: 1.6;
  color: var(--vp-c-text-1);
}

@media (max-width: 960px) {
  .builder-layout {
    grid-template-columns: 1fr;
  }

  .form-section {
    border-right: none;
    border-bottom: 1px solid var(--vp-c-divider);
    max-height: 400px;
  }
}

@media (max-width: 640px) {
  .form-section,
  .preview-section {
    padding: 1rem;
  }

  .sub-input {
    margin-left: 0;
    width: 100%;
  }
}
</style>
