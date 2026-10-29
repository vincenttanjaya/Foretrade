import { createContext, use, useState, type ReactNode } from 'react'

export type Theme = 'light' | 'dark'

const ThemeContext = createContext<{ theme: Theme; setTheme: (t: Theme) => void; toggle: () => void } | null>(null)

export function ThemeProvider({ children }: { children: ReactNode }) {
  // index.html already applied the saved / OS theme to <html data-theme>.
  const [theme, setThemeState] = useState<Theme>(() =>
    document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light',
  )

  const setTheme = (t: Theme) => {
    // Set the attribute before re-rendering: child effects (e.g. LineChart) read the CSS variables
    // and run before any effect up here would.
    document.documentElement.dataset.theme = t
    setThemeState(t)
    try {
      localStorage.setItem('theme', t)
    } catch {
      // storage blocked (private mode): the choice lasts for this page only
    }
  }

  return (
    <ThemeContext value={{ theme, setTheme, toggle: () => setTheme(theme === 'dark' ? 'light' : 'dark') }}>
      {children}
    </ThemeContext>
  )
}

export function useTheme() {
  const ctx = use(ThemeContext)
  if (!ctx) throw new Error('useTheme must be used inside <ThemeProvider>')
  return ctx
}
