import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'
import { App } from './App'
import { ToastProvider } from './components/toast'
import { AuthProvider } from './lib/auth'
import { ThemeProvider } from './lib/theme'
import './index.css'

// A deploy deletes the previous build's hashed chunks, so a tab still on the old index.html fails to load a route it
// hasn't opened yet: reload to pick up the new build. At most once per 10 s, so a chunk that is still missing after
// the reload falls through to the page error boundary (with its Reload button) instead of looping.
window.addEventListener('vite:preloadError', () => {
  try {
    if (Date.now() - Number(sessionStorage.getItem('chunk-reload')) < 10_000) return
    sessionStorage.setItem('chunk-reload', String(Date.now()))
  } catch {
    return // storage blocked: no loop guard, so leave it to the error boundary
  }
  location.reload()
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <ThemeProvider>
        <ToastProvider>
          <AuthProvider>
            <App />
          </AuthProvider>
        </ToastProvider>
      </ThemeProvider>
    </BrowserRouter>
  </StrictMode>,
)
