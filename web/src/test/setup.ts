// Vitest setup (vite.config.ts → test.setupFiles): DOM matchers, jsdom gaps, and no real network in any test.
import '@testing-library/jest-dom/vitest'
import { cleanup, configure } from '@testing-library/react'
import { afterEach, beforeEach, vi } from 'vitest'
import { FakeWebSocket } from './socket'

// A cold worker can take a few seconds to transform a lazy route chunk; findBy*/waitFor wait up to 5 s.
configure({ asyncUtilTimeout: 5000 })

// lightweight-charts draws on <canvas>, which jsdom lacks; every test gets the recording fake.
vi.mock('lightweight-charts', () => import('./chart'))

// jsdom has no scrolling, popover or modal-dialog API; the app only needs them not to throw.
Element.prototype.scrollTo ??= function () {}
HTMLElement.prototype.showPopover ??= function () {}
HTMLElement.prototype.hidePopover ??= function () {}
HTMLDialogElement.prototype.showModal ??= function (this: HTMLDialogElement) {
  this.setAttribute('open', '')
}
HTMLDialogElement.prototype.close ??= function (this: HTMLDialogElement) {
  this.removeAttribute('open')
  this.dispatchEvent(new Event('close'))
}

beforeEach(() => {
  // Unmocked requests fail like an unreachable API; the Binance socket never connects.
  vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new TypeError('fetch is not mocked in this test'))))
  FakeWebSocket.instances = []
  vi.stubGlobal('WebSocket', FakeWebSocket)
  localStorage.clear()
  sessionStorage.clear() // the instrument catalogue keeps a per-session copy
  delete document.documentElement.dataset.theme
})

afterEach(cleanup)
