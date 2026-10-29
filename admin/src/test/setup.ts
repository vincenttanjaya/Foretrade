// Vitest setup (vite.config.ts → test.setupFiles): DOM matchers, jsdom gaps, and no real network in any test.
import '@testing-library/jest-dom/vitest'
import { cleanup, configure } from '@testing-library/react'
import { afterEach, beforeEach, vi } from 'vitest'

configure({ asyncUtilTimeout: 5000 })

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
  // Unmocked requests fail like an unreachable API.
  vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new TypeError('fetch is not mocked in this test'))))
  localStorage.clear()
  delete document.documentElement.dataset.theme
})

afterEach(cleanup)
