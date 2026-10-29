import { useEffect, useId, useState } from 'react'
import { Button } from '../../components/ui/Button'
import { Card, CardHeader } from '../../components/ui/Card'
import { Input, Label } from '../../components/ui/Input'
import { api, ApiError } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import type { ChannelTestResult, NotificationChannels, UpdateNotificationChannels } from '../../lib/types'

const LARK_HOOK = /^https:\/\/open\.(larksuite\.com|feishu\.cn)\/open-apis\/bot\/v2\/hook\/[^/?#\s]+$/

/**
 * Account → Notifications: the bell's alerts also by email and to the user's own Lark custom bot. Hidden when the API
 * predates channels (404).
 */
export function NotificationSettings() {
  const id = useId()
  const [channels, setChannels] = useState<NotificationChannels | null>(null)
  const [hidden, setHidden] = useState(false)
  const [webhook, setWebhook] = useState('')
  const [secret, setSecret] = useState('')
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [test, setTest] = useState<ChannelTestResult | null>(null)

  useEffect(() => {
    const ctl = new AbortController()
    api.notificationChannels(ctl.signal).then(({ channels }) => setChannels(channels)).catch((err) => {
      if (ctl.signal.aborted) return
      if (err instanceof ApiError && err.status === 404) setHidden(true)
      else setError(errorMessage(err, 'Could not load your notification settings.'))
    })
    return () => ctl.abort()
  }, [])

  async function update(body: UpdateNotificationChannels, what: string, done: string) {
    if (busy) return
    setBusy(what)
    setError('')
    setNotice('')
    setTest(null)
    try {
      const { channels } = await api.updateNotificationChannels(body)
      setChannels(channels)
      setNotice(done)
      return true
    } catch (err) {
      setError(errorMessage(err, 'Could not save your notification settings.'))
    } finally {
      setBusy(null)
    }
  }

  async function saveLark() {
    const hook = webhook.trim()
    if (hook && !LARK_HOOK.test(hook)) {
      setError('Paste the webhook URL of a Lark custom bot (https://open.larksuite.com/open-apis/bot/v2/hook/…).')
      return
    }
    const body: UpdateNotificationChannels = { lark: true }
    if (hook) body.larkWebhook = hook
    if (secret.trim()) body.larkSecret = secret.trim()
    if (await update(body, 'lark', 'Lark notifications are on.')) {
      setWebhook('')
      setSecret('')
    }
  }

  async function sendTest() {
    if (busy) return
    setBusy('test')
    setError('')
    setNotice('')
    try {
      setTest(await api.testNotificationChannels())
    } catch (err) {
      setError(errorMessage(err, 'Could not send a test notification.'))
    } finally {
      setBusy(null)
    }
  }

  if (hidden) return null
  const lark = channels?.lark
  return (
    <Card className="mt-6">
      <CardHeader label="Notifications" title="Alerts outside the app" />
      <div className="space-y-5 px-5 pb-5">
        <p className="text-[13px] leading-relaxed text-muted">
          Everything in the bell (price, odds and drawdown alerts, scheduled briefs, stop and target fills, forecast results) can also reach you by
          email and in Lark, within about 15 seconds. Only alerts from after you turn a channel on are sent.
        </p>
        {!channels && !error && <p className="text-sm text-muted">Loading…</p>}
        {channels && (
          <>
            <section aria-label="Email notifications" className="space-y-1.5">
              <label className="flex items-center gap-3 text-sm font-medium">
                <input
                  type="checkbox"
                  className="size-4 accent-accent"
                  checked={channels.email.enabled}
                  disabled={busy != null || (!channels.email.available && !channels.email.enabled)}
                  onChange={(e) => update({ email: e.target.checked }, 'email', e.target.checked ? 'Email notifications are on.' : 'Email notifications are off.')}
                />
                Email me at {channels.email.address}
              </label>
              {!channels.email.available && <p className="text-[13px] text-muted">Email isn't set up on this server yet.</p>}
            </section>

            <section aria-label="Lark notifications" className="space-y-3 border-t border-border pt-5">
              <label className="flex items-center gap-3 text-sm font-medium">
                <input
                  type="checkbox"
                  className="size-4 accent-accent"
                  checked={!!lark?.enabled}
                  disabled={busy != null || !lark?.webhook}
                  onChange={(e) => update({ lark: e.target.checked }, 'lark', e.target.checked ? 'Lark notifications are on.' : 'Lark notifications are off.')}
                />
                Post to my Lark bot
              </label>
              {lark?.webhook && (
                <p className="text-[13px] text-muted [overflow-wrap:anywhere]">
                  Bot: {lark.webhook}
                  {lark.signed ? ' · signed' : ''}
                </p>
              )}
              <p className="text-[13px] leading-relaxed text-muted">
                In Lark, open a group (or a chat with just you), then Settings → Bots → Add Bot → Custom Bot, and copy its webhook address. If you turn on
                signature verification, paste the secret too.
              </p>
              <div className="grid gap-3 sm:grid-cols-2">
                <div>
                  <Label htmlFor={`${id}-hook`}>{lark?.webhook ? 'Replace webhook URL' : 'Webhook URL'}</Label>
                  <Input
                    id={`${id}-hook`}
                    inputMode="url"
                    autoComplete="off"
                    placeholder="https://open.larksuite.com/open-apis/bot/v2/hook/…"
                    value={webhook}
                    onChange={(e) => setWebhook(e.target.value)}
                  />
                </div>
                <div>
                  <Label htmlFor={`${id}-secret`}>Signing secret (optional)</Label>
                  <Input id={`${id}-secret`} type="password" autoComplete="off" value={secret} onChange={(e) => setSecret(e.target.value)} />
                </div>
              </div>
              <div className="flex flex-wrap gap-2">
                <Button onClick={saveLark} loading={busy === 'lark'} disabled={busy != null || (!webhook.trim() && !lark?.webhook)}>
                  Save and turn on Lark
                </Button>
                {lark?.webhook && (
                  <Button
                    variant="secondary"
                    disabled={busy != null}
                    onClick={() => update({ lark: false, larkWebhook: '', larkSecret: '' }, 'remove', 'Lark bot removed.')}
                  >
                    Remove bot
                  </Button>
                )}
              </div>
            </section>

            <div className="flex flex-wrap items-center gap-3 border-t border-border pt-5">
              <Button variant="secondary" onClick={sendTest} loading={busy === 'test'} disabled={busy != null || (!channels.email.enabled && !lark?.enabled)}>
                Send a test
              </Button>
              {test && (
                <p role="status" className="text-[13px] text-muted [overflow-wrap:anywhere]">
                  Email: {test.email} · Lark: {test.lark}
                </p>
              )}
            </div>
          </>
        )}
        {error && <p role="alert" className="text-[13px] text-down">{error}</p>}
        {notice && <p role="status" className="text-[13px] text-muted">{notice}</p>}
      </div>
    </Card>
  )
}
