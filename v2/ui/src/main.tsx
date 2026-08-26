import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { App } from './App.tsx'
import { ToastProvider } from './components/ui/Toast.tsx'
import { initializeTheme } from './lib/theme.ts'
import '@fontsource-variable/inter'
import '@fontsource/jetbrains-mono'
import '@xyflow/react/dist/style.css'
import './styles/tokens.css'
import './style.css'
import './styles/ui.css'

initializeTheme()

const queryClient = new QueryClient()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <App />
      </ToastProvider>
    </QueryClientProvider>
  </StrictMode>,
)
