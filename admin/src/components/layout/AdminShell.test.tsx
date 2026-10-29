import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Link, MemoryRouter, Route, Routes } from 'react-router'
import { describe, expect, it, vi } from 'vitest'
import { AuthProvider } from '../../lib/auth'
import { ThemeProvider } from '../../lib/theme'
import { stubSession } from '../../test/utils'
import { AdminShell } from './AdminShell'

function Boom(): never {
  throw new Error('render failed')
}

describe('AdminShell', () => {
  it('keeps the shell up when a page throws, and recovers on navigation', async () => {
    stubSession()
    vi.spyOn(console, 'error').mockImplementation(() => {}) // React logs the caught error
    const user = userEvent.setup()
    render(
      <MemoryRouter initialEntries={['/users']}>
        <ThemeProvider>
          <AuthProvider>
            <Routes>
              <Route element={<AdminShell />}>
                <Route path="users" element={<Boom />} />
                <Route path="audit" element={<h1>Audit page</h1>} />
              </Route>
            </Routes>
            <Link to="/audit">go</Link>
          </AuthProvider>
        </ThemeProvider>
      </MemoryRouter>,
    )
    expect(await screen.findByText('This page failed to load.')).toBeInTheDocument()
    expect(screen.getAllByRole('link', { name: 'Users' })[0]).toHaveAttribute('aria-current', 'page')
    expect(document.title).toBe('Users · Foretrade Admin')
    await user.click(screen.getByRole('link', { name: 'go' }))
    expect(await screen.findByRole('heading', { name: 'Audit page' })).toBeInTheDocument()
    expect(screen.queryByText('This page failed to load.')).not.toBeInTheDocument()
  })
})
