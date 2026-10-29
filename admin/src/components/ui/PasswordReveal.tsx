import { useState } from 'react'
import { useToast } from '../toast'
import { Button } from './Button'
import { IconAlert, IconCheck, IconCopy } from './icons'

/**
 * A server-generated password, shown once. It only ever lives in the open modal's state: never in the URL, storage,
 * logs or analytics. The parent clears it when the modal closes.
 */
export function PasswordReveal({ email, password, onDone }: { email: string; password: string; onDone: () => void }) {
  const { toast } = useToast()
  const [copied, setCopied] = useState(false)

  async function copy() {
    try {
      await navigator.clipboard.writeText(password)
      setCopied(true)
    } catch {
      toast("Couldn't copy. Select the password and copy it by hand.", 'error')
    }
  }

  return (
    <div className="mt-4">
      <div className="flex items-start gap-2.5 rounded-lg border border-accent/40 bg-accent-soft px-3 py-2.5 text-[13.5px] leading-snug text-text">
        <IconAlert size={16} className="mt-px shrink-0 text-accent-ink" />
        <p>
          <span className="font-semibold">Shown once.</span> Copy it now and share it with{' '}
          <span className="font-medium break-all">{email}</span> over a secure channel. It can't be shown again; you would
          have to set a new one.
        </p>
      </div>
      <label htmlFor="temp-password" className="label mt-5 mb-1.5 block">
        Temporary password
      </label>
      <div className="flex flex-col gap-2 sm:flex-row">
        <input
          id="temp-password"
          readOnly
          autoFocus // the form that opened it is gone: keep focus (and screen readers) inside the dialog, text selected
          value={password}
          autoComplete="off"
          spellCheck={false}
          onFocus={(e) => e.currentTarget.select()}
          className="h-10 w-full min-w-0 rounded-lg border border-border bg-surface-2 sm:flex-1 px-3 font-mono text-sm tracking-wide text-text focus:border-accent focus:ring-[3px] focus:ring-accent/20 focus:outline-none"
        />
        <Button variant="secondary" onClick={copy}>
          {copied ? <IconCheck size={16} /> : <IconCopy size={16} />}
          {copied ? 'Copied' : 'Copy'}
        </Button>
      </div>
      <div className="mt-6 flex justify-end">
        <Button onClick={onDone}>I've saved it</Button>
      </div>
    </div>
  )
}
