import { screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { NotificationSettings } from './NotificationSettings'

const hook = 'https://open.larksuite.com/open-apis/bot/v2/hook/0a1b2c3d-4e5f'
const masked = 'https://open.larksuite.com/open-apis/bot/v2/hook/…4e5f'

function open(channels = fx.notificationChannels()) {
  stubSession().notificationChannels.mockResolvedValue({ channels })
  return renderPage(<NotificationSettings />)
}

describe('Account notifications', () => {
  it('turns email on with one click', async () => {
    const put = vi.spyOn(api, 'updateNotificationChannels').mockResolvedValue({
      channels: fx.notificationChannels({ email: { enabled: true, address: 'demo@example.com', available: true } }),
    })
    const { user } = open()
    await user.click(await screen.findByRole('checkbox', { name: 'Email me at demo@example.com' }))
    expect(put).toHaveBeenCalledWith({ email: true })
    expect(await screen.findByText('Email notifications are on.')).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: 'Email me at demo@example.com' })).toBeChecked()
  })

  it('keeps email off when the server cannot send it', async () => {
    open(fx.notificationChannels({ email: { enabled: false, address: 'demo@example.com', available: false } }))
    expect(await screen.findByRole('checkbox', { name: 'Email me at demo@example.com' })).toBeDisabled()
    expect(screen.getByText("Email isn't set up on this server yet.")).toBeInTheDocument()
  })

  it('saves a Lark bot with its secret, shows it masked, and refuses a foreign URL without a request', async () => {
    const put = vi.spyOn(api, 'updateNotificationChannels').mockResolvedValue({
      channels: fx.notificationChannels({ lark: { enabled: true, webhook: masked, signed: true } }),
    })
    const { user } = open()
    const toggle = await screen.findByRole('checkbox', { name: 'Post to my Lark bot' })
    expect(toggle).toBeDisabled() // no bot yet
    await user.type(screen.getByLabelText('Webhook URL'), 'https://evil.example/hook')
    await user.click(screen.getByRole('button', { name: 'Save and turn on Lark' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Lark custom bot')
    expect(put).not.toHaveBeenCalled()

    await user.clear(screen.getByLabelText('Webhook URL'))
    await user.type(screen.getByLabelText('Webhook URL'), hook)
    await user.type(screen.getByLabelText('Signing secret (optional)'), 's3cret')
    await user.click(screen.getByRole('button', { name: 'Save and turn on Lark' }))
    expect(put).toHaveBeenCalledWith({ lark: true, larkWebhook: hook, larkSecret: 's3cret' })
    expect(await screen.findByText(`Bot: ${masked} · signed`)).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: 'Post to my Lark bot' })).toBeChecked()
    expect(screen.getByLabelText('Replace webhook URL')).toHaveValue('')
  })

  it('sends a test and shows each channel outcome', async () => {
    const test = vi.spyOn(api, 'testNotificationChannels').mockResolvedValue({ email: 'off', lark: 'lark: sign match fail (code 19021)' })
    const { user } = open(fx.notificationChannels({ lark: { enabled: true, webhook: masked, signed: true } }))
    await user.click(await screen.findByRole('button', { name: 'Send a test' }))
    expect(test).toHaveBeenCalled()
    expect(await screen.findByText('Email: off · Lark: lark: sign match fail (code 19021)')).toBeInTheDocument()
  })

  it('removes the bot and shows server errors', async () => {
    const put = vi.spyOn(api, 'updateNotificationChannels')
      .mockRejectedValueOnce(new ApiError(400, 'bad_request', 'Add your Lark bot\'s webhook URL to turn Lark on.'))
      .mockResolvedValueOnce({ channels: fx.notificationChannels() })
    const { user } = open(fx.notificationChannels({ lark: { enabled: true, webhook: masked, signed: false } }))
    await user.click(await screen.findByRole('checkbox', { name: 'Post to my Lark bot' }))
    expect(await screen.findByRole('alert')).toHaveTextContent("Add your Lark bot's webhook URL to turn Lark on.")
    await user.click(screen.getByRole('button', { name: 'Remove bot' }))
    expect(put).toHaveBeenLastCalledWith({ lark: false, larkWebhook: '', larkSecret: '' })
    expect(await screen.findByText('Lark bot removed.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Remove bot' })).not.toBeInTheDocument()
  })

  it('stays hidden against an API without channels', async () => {
    stubSession().notificationChannels.mockRejectedValue(new ApiError(404, 'not_found', 'Not found'))
    const { container } = renderPage(<NotificationSettings />)
    await waitFor(() => expect(api.notificationChannels).toHaveBeenCalled())
    await waitFor(() => expect(container.querySelector('section')).toBeNull())
    expect(screen.queryByText('Alerts outside the app')).not.toBeInTheDocument()
  })
})
