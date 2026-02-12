<template>
  <div class="pipeline-simulator">
    <div class="controls">
      <button @click="startPipeline" :disabled="isRunning" class="btn btn-start">
        Start Pipeline
      </button>
      <button @click="resetPipeline" class="btn btn-reset">
        Reset
      </button>
    </div>

    <div class="pipeline-steps">
      <div
        v-for="step in steps"
        :key="step.name"
        class="step"
        :class="[`status-${step.status}`]"
        @click="failStep(step)"
      >
        <div class="step-indicator">
          <div class="status-dot" :class="{ pulse: step.status === 'running' }"></div>
        </div>
        <div class="step-content">
          <div class="step-name">{{ step.name }}</div>
          <div class="step-status">{{ getStatusText(step.status) }}</div>
        </div>
      </div>
    </div>

    <div v-if="pipelineStatus" class="pipeline-result" :class="`result-${pipelineStatus}`">
      {{ getPipelineMessage() }}
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'

const steps = ref([
  { name: 'clone', status: 'pending', duration: 800 },
  { name: 'build', status: 'pending', duration: 1200 },
  { name: 'test', status: 'pending', duration: 1000 },
  { name: 'snapshot', status: 'pending', duration: 900 },
  { name: 'migrate', status: 'pending', duration: 700 },
  { name: 'deploy', status: 'pending', duration: 1100 },
  { name: 'cleanup', status: 'pending', duration: 600 }
])

const isRunning = ref(false)
const pipelineStatus = ref(null)

const getStatusText = (status) => {
  const statusMap = {
    pending: 'pending',
    running: 'running...',
    done: 'done',
    failed: 'failed'
  }
  return statusMap[status] || status
}

const getPipelineMessage = () => {
  if (pipelineStatus.value === 'success') {
    return '✓ Pipeline completed successfully'
  } else if (pipelineStatus.value === 'failed') {
    return '✗ Pipeline failed'
  }
  return ''
}

const startPipeline = async () => {
  if (isRunning.value) return

  isRunning.value = true
  pipelineStatus.value = null

  for (let i = 0; i < steps.value.length; i++) {
    const step = steps.value[i]

    if (step.status === 'failed') {
      pipelineStatus.value = 'failed'
      isRunning.value = false
      return
    }

    step.status = 'running'

    await new Promise(resolve => setTimeout(resolve, step.duration))

    if (step.status === 'failed') {
      pipelineStatus.value = 'failed'
      isRunning.value = false
      return
    }

    step.status = 'done'
  }

  pipelineStatus.value = 'success'
  isRunning.value = false
}

const failStep = (step) => {
  if (isRunning.value && step.status === 'running') {
    step.status = 'failed'
  }
}

const resetPipeline = () => {
  isRunning.value = false
  pipelineStatus.value = null
  steps.value.forEach(step => {
    step.status = 'pending'
  })
}
</script>

<style scoped>
.pipeline-simulator {
  padding: 1.5rem;
  border: 1px solid var(--vp-c-divider);
  border-radius: 8px;
  background: var(--vp-c-bg-soft);
  margin: 1rem 0;
}

.controls {
  display: flex;
  gap: 0.75rem;
  margin-bottom: 1.5rem;
}

.btn {
  padding: 0.5rem 1rem;
  border: 1px solid var(--vp-c-divider);
  border-radius: 4px;
  background: var(--vp-c-bg);
  color: var(--vp-c-text-1);
  cursor: pointer;
  font-size: 0.9rem;
  font-weight: 500;
  transition: all 0.2s;
}

.btn:hover:not(:disabled) {
  background: var(--vp-c-bg-soft);
  border-color: var(--vp-c-brand);
}

.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.btn-start {
  background: var(--vp-c-brand);
  color: white;
  border-color: var(--vp-c-brand);
}

.btn-start:hover:not(:disabled) {
  background: var(--vp-c-brand-dark);
}

.pipeline-steps {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}

.step {
  display: flex;
  align-items: center;
  gap: 1rem;
  padding: 1rem;
  border-radius: 6px;
  background: var(--vp-c-bg);
  border: 2px solid transparent;
  transition: all 0.2s;
}

.step.status-running {
  cursor: pointer;
  border-color: #f59e0b;
  background: rgba(245, 158, 11, 0.05);
}

.step.status-running:hover {
  background: rgba(245, 158, 11, 0.1);
}

.step-indicator {
  flex-shrink: 0;
}

.status-dot {
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: #6b7280;
  transition: background 0.3s;
}

.status-pending .status-dot {
  background: #6b7280;
}

.status-running .status-dot {
  background: #f59e0b;
}

.status-done .status-dot {
  background: #10b981;
}

.status-failed .status-dot {
  background: #ef4444;
}

.pulse {
  animation: pulse 1.5s cubic-bezier(0.4, 0, 0.6, 1) infinite;
}

@keyframes pulse {
  0%, 100% {
    opacity: 1;
  }
  50% {
    opacity: 0.5;
  }
}

.step-content {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.step-name {
  font-weight: 600;
  font-size: 0.95rem;
  color: var(--vp-c-text-1);
  font-family: var(--vp-font-family-mono);
}

.step-status {
  font-size: 0.85rem;
  color: var(--vp-c-text-2);
  font-family: var(--vp-font-family-mono);
}

.pipeline-result {
  margin-top: 1.5rem;
  padding: 1rem;
  border-radius: 6px;
  text-align: center;
  font-weight: 600;
}

.result-success {
  background: rgba(16, 185, 129, 0.1);
  color: #10b981;
  border: 1px solid #10b981;
}

.result-failed {
  background: rgba(239, 68, 68, 0.1);
  color: #ef4444;
  border: 1px solid #ef4444;
}

@media (max-width: 640px) {
  .pipeline-simulator {
    padding: 1rem;
  }

  .step-content {
    flex-direction: column;
    align-items: flex-start;
    gap: 0.25rem;
  }

  .step {
    padding: 0.75rem;
  }
}
</style>
