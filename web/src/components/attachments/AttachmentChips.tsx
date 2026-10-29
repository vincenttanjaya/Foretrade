// The attachment chips the chat composer and the forecast materials field share: a preview or type tile, the name,
// "type · size", a remove button; then the per-file errors and the limits line.
import type { ReactNode } from 'react'
import { IconX } from '../ui/icons'
import { cx } from '../../lib/cx'
import { attachmentLabel, formatBytes } from '../../pages/agent/attachments'
import { FileGlyph } from '../../pages/agent/FileGlyph'
import type { Selection } from './useAttachmentQueue'

const CHIP = 'flex max-w-full min-w-0 items-center gap-2 rounded-lg border border-border bg-surface-2 p-1.5 text-[12px]'
const REMOVE = 'grid size-8 shrink-0 place-items-center rounded-full text-muted hover:bg-surface hover:text-text disabled:opacity-45'

/** One chip: `tile` is the preview, the type glyph or the "Preparing…" status; `meta` the line under the name. */
export function AttachmentChip({ name, tile, meta, disabled, onRemove }: { name: string; tile: ReactNode; meta?: ReactNode; disabled?: boolean; onRemove: () => void }) {
  return (
    <li className={CHIP}>
      {tile}
      <span className="flex min-w-0 flex-col leading-tight">
        <span className="max-w-40 truncate text-text">{name}</span>
        {meta && <span className="text-[11px] text-muted">{meta}</span>}
      </span>
      <button type="button" aria-label={`Remove ${name}`} title={`Remove ${name}`} disabled={disabled} onClick={onRemove} className={REMOVE}>
        <IconX size={14} />
      </button>
    </li>
  )
}

/** A queued file's tile: its image preview, its type glyph once read, or a "Preparing…" status while it converts. */
export function SelectionTile({ file }: { file: Selection }) {
  if (file.url) return <img src={file.url} alt={`Preview ${file.name}`} className="size-10 shrink-0 rounded object-cover" />
  if (file.input) return <FileGlyph attachment={file.input} className="text-accent" />
  return (
    <span role="status" className="grid h-10 shrink-0 place-items-center px-1 text-muted">
      Preparing…
    </span>
  )
}

/**
 * The queue's chips (after any `leading` chips, e.g. files read on an earlier run), then its errors (an alert with
 * `errorId`, for the input's aria-describedby) and the `help` line (`helpId`). The list only renders with chips.
 */
export function AttachmentChips({
  files,
  errors,
  disabled,
  onRemove,
  leading,
  help,
  helpId,
  errorId,
  listClassName,
  errorClassName,
  helpClassName,
}: {
  files: Selection[]
  errors: string[]
  disabled?: boolean
  onRemove: (key: number) => void
  leading?: ReactNode[]
  help?: ReactNode
  helpId?: string
  errorId?: string
  listClassName?: string
  errorClassName?: string
  helpClassName?: string
}) {
  const before = (leading ?? []).filter(Boolean)
  return (
    <>
      {files.length + before.length > 0 && (
        <ul aria-label="Attached files" className={cx('flex flex-wrap gap-2', listClassName)}>
          {before}
          {files.map((file) => (
            <AttachmentChip
              key={file.key}
              name={file.name}
              tile={<SelectionTile file={file} />}
              meta={file.input && `${attachmentLabel(file.input)} · ${formatBytes(file.size ?? 0)}`}
              disabled={disabled}
              onRemove={() => onRemove(file.key)}
            />
          ))}
        </ul>
      )}
      {errors.length > 0 && (
        <div id={errorId} role="alert" className={cx('space-y-0.5 text-[12.5px] [overflow-wrap:anywhere] text-down', errorClassName)}>
          {errors.map((error, i) => (
            <p key={i}>{error}</p>
          ))}
        </div>
      )}
      {help && (
        <p id={helpId} className={cx('leading-snug text-muted', helpClassName)}>
          {help}
        </p>
      )}
    </>
  )
}
