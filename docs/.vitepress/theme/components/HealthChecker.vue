<template>
  <div class="health-checker">
    <div class="terminal">
      <div class="terminal-header">
        <span class="terminal-title">$ norn health</span>
      </div>

      <div class="terminal-body">
        <div
          v-for="service in services"
          :key="service.name"
          class="service-item"
        >
          <div class="service-header">
            <div class="service-info">
              <span class="status-indicator" :class="service.healthy ? 'healthy' : 'unhealthy'">
                {{ service.healthy ? '✓' : '✗' }}
              </span>
              <span class="service-name">{{ service.name }}</span>
              <span class="service-status" :class="service.healthy ? 'status-ok' : 'status-error'">
                {{ service.healthy ? service.healthyMessage : service.errorMessage }}
              </span>
            </div>
            <label class="toggle-switch">
              <input
                type="checkbox"
                v-model="service.healthy"
              />
              <span class="slider"></span>
            </label>
          </div>

          <div v-if="!service.healthy" class="service-error">
            <div class="error-details">
              <div class="error-label">Fix:</div>
              <div class="error-message">{{ service.fixInstructions }}</div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'

const services = ref([
  {
    name: 'PostgreSQL',
    healthy: true,
    healthyMessage: 'running',
    errorMessage: 'not running',
    fixInstructions: 'Install and start Postgres.app, then run: make db'
  },
  {
    name: 'Kubernetes',
    healthy: true,
    healthyMessage: 'connected',
    errorMessage: 'not connected',
    fixInstructions: 'Start minikube or configure kubectl context. Norn works without K8s for local development.'
  },
  {
    name: 'Valkey',
    healthy: true,
    healthyMessage: 'running',
    errorMessage: 'not running',
    fixInstructions: 'Run: make infra'
  },
  {
    name: 'Redpanda',
    healthy: true,
    healthyMessage: 'running',
    errorMessage: 'not running',
    fixInstructions: 'Run: make infra'
  },
  {
    name: 'SOPS age key',
    healthy: true,
    healthyMessage: 'configured',
    errorMessage: 'missing',
    fixInstructions: 'Run: age-keygen -o ~/.config/sops/age/keys.txt'
  },
  {
    name: 'Norn API',
    healthy: true,
    healthyMessage: 'running',
    errorMessage: 'not running',
    fixInstructions: 'Run: make dev'
  }
])
</script>

<style scoped>
.health-checker {
  margin: 1rem 0;
}

.terminal {
  border-radius: 8px;
  overflow: hidden;
  background: #1e1e1e;
  border: 1px solid #333;
  font-family: var(--vp-font-family-mono);
}

.terminal-header {
  background: #2d2d2d;
  padding: 0.75rem 1rem;
  border-bottom: 1px solid #333;
}

.terminal-title {
  color: #a0a0a0;
  font-size: 0.9rem;
}

.terminal-body {
  padding: 1rem;
}

.service-item {
  margin-bottom: 1rem;
  padding-bottom: 1rem;
  border-bottom: 1px solid #333;
}

.service-item:last-child {
  margin-bottom: 0;
  padding-bottom: 0;
  border-bottom: none;
}

.service-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 1rem;
}

.service-info {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  flex: 1;
}

.status-indicator {
  font-size: 1rem;
  font-weight: bold;
  width: 20px;
  text-align: center;
}

.status-indicator.healthy {
  color: #10b981;
}

.status-indicator.unhealthy {
  color: #ef4444;
}

.service-name {
  color: #e0e0e0;
  font-weight: 600;
  min-width: 140px;
}

.service-status {
  font-size: 0.85rem;
}

.status-ok {
  color: #10b981;
}

.status-error {
  color: #ef4444;
}

.toggle-switch {
  position: relative;
  display: inline-block;
  width: 44px;
  height: 24px;
  flex-shrink: 0;
}

.toggle-switch input {
  opacity: 0;
  width: 0;
  height: 0;
}

.slider {
  position: absolute;
  cursor: pointer;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background-color: #ef4444;
  transition: 0.3s;
  border-radius: 24px;
}

.slider:before {
  position: absolute;
  content: "";
  height: 18px;
  width: 18px;
  left: 3px;
  bottom: 3px;
  background-color: white;
  transition: 0.3s;
  border-radius: 50%;
}

input:checked + .slider {
  background-color: #10b981;
}

input:checked + .slider:before {
  transform: translateX(20px);
}

.service-error {
  margin-top: 0.75rem;
  margin-left: 2rem;
}

.error-details {
  background: rgba(239, 68, 68, 0.1);
  border-left: 3px solid #ef4444;
  padding: 0.75rem;
  border-radius: 4px;
}

.error-label {
  color: #f59e0b;
  font-weight: 600;
  font-size: 0.85rem;
  margin-bottom: 0.4rem;
}

.error-message {
  color: #d0d0d0;
  font-size: 0.85rem;
  line-height: 1.5;
  font-family: var(--vp-font-family-mono);
}

@media (max-width: 640px) {
  .service-header {
    flex-wrap: wrap;
  }

  .service-info {
    flex-direction: column;
    align-items: flex-start;
    gap: 0.5rem;
  }

  .service-name {
    min-width: auto;
  }

  .toggle-switch {
    align-self: flex-end;
  }

  .service-error {
    margin-left: 0;
  }
}
</style>
