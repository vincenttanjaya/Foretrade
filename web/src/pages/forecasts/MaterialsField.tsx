// "Materials (optional)" (API contract round 9): files the desk reads once at the start of the run. A drop zone that
// also takes a paste while focused, the picker, one chip per file and each file's own error.
import { useId, useState, type ReactNode } from 'react'
import { AttachmentChip, AttachmentChips } from '../../components/attachments/AttachmentChips'
import type { AttachmentQueue } from '../../components/attachments/useAttachmentQueue'
import { buttonClass } from '../../components/ui/Button'
import { IconPaperclip } from '../../components/ui/icons'
import { cx } from '../../lib/cx'
import type { ForecastMaterial } from '../../lib/types'
import { ACCEPT, pastesText } from '../agent/attachments'
import { FileGlyph } from '../agent/FileGlyph'
import { MAX_MATERIALS } from './forecast'

export const MATERIALS_HELP =
  'Up to 5 files: PDF, Word, Excel, CSV, text, Markdown or HTML (4.5 MB each), or photos and charts (converted to JPEG, 1.5 MB). Files are read once; we keep the extracted notes, not the file. +1 credit per file.'

/** The files on a clipboard paste: its files, else its file items (some browsers only fill `items`). */
export function pastedFiles(data: DataTransfer): File[] {
  const files = Array.from(data.files ?? [])
  if (!files.length)
    for (const item of Array.from(data.items ?? [])) {
      if (item.kind === 'file') {
        const file = item.getAsFile()
        if (file) files.push(file)
      }
    }
  return !files.length || pastesText(data, files) ? [] : files
}

/**
 * The materials field. `queue` belongs to the caller (it sends the files); `kept` are materials the desk already read
 * (an update keeps them unless removed, at no cost). `note` is a muted line under the chips.
 */
export function MaterialsField({
  queue,
  kept = [],
  onRemoveKept,
  disabled,
  note,
  label = 'Materials (optional)',
}: {
  queue: AttachmentQueue
  kept?: ForecastMaterial[]
  onRemoveKept?: (id: string) => void
  disabled?: boolean
  note?: ReactNode
  label?: string
}) {
  const id = useId()
  const [dragging, setDragging] = useState(false)
  const count = kept.length + queue.files.length
  const full = count >= MAX_MATERIALS
  const describedBy = `${id}-help${queue.errors.length ? ` ${id}-error` : ''}`
  return (
    <div>
      <div className="flex items-baseline justify-between">
        <span id={`${id}-label`} className="label mb-1.5 block">
          {label}
        </span>
        <span className="font-mono text-[11px] text-muted tabular-nums">
          <span aria-hidden="true">
            {count}/{MAX_MATERIALS}
          </span>
          <span className="sr-only">
            {count} of {MAX_MATERIALS} files
          </span>
        </span>
      </div>
      <div
        role="group"
        tabIndex={disabled ? -1 : 0}
        aria-label="Materials: drop or paste files"
        aria-describedby={`${id}-help`}
        onDragOver={(e) => {
          if (!e.dataTransfer.types.includes('Files')) return
          e.preventDefault()
          if (!disabled) setDragging(true)
        }}
        onDragLeave={(e) => {
          if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDragging(false)
        }}
        onDrop={(e) => {
          setDragging(false)
          if (!e.dataTransfer.files.length) return
          e.preventDefault()
          queue.attach(Array.from(e.dataTransfer.files))
        }}
        onPaste={(e) => {
          const files = pastedFiles(e.clipboardData)
          if (!files.length) return
          e.preventDefault()
          queue.attach(files)
        }}
        className={cx(
          'rounded-xl border border-dashed border-border bg-surface-2/40 px-4 py-3.5 transition-[border-color,box-shadow] duration-150 outline-none',
          'focus-visible:border-accent focus-visible:ring-[3px] focus-visible:ring-accent/20',
          dragging && 'border-accent ring-[3px] ring-accent/20',
          disabled && 'opacity-60',
        )}
      >
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          <span className="text-[13.5px] text-text">Drop files here or</span>
          {/* A label around the input opens the native file picker; the input stays focusable for keyboards. */}
          <label
            className={buttonClass(
              'secondary',
              'sm',
              cx('cursor-pointer focus-within:ring-2 focus-within:ring-accent', (disabled || full) && 'pointer-events-none opacity-45'),
            )}
          >
            <IconPaperclip size={13} />
            Add material
            <input
              type="file"
              aria-label="Add material"
              aria-describedby={describedBy}
              accept={ACCEPT}
              multiple
              disabled={disabled || full}
              className="sr-only"
              onChange={(e) => {
                queue.attach(Array.from(e.target.files ?? []))
                e.target.value = '' // the same file can be attached again after a remove
              }}
            />
          </label>
        </div>
        <p id={`${id}-help`} className="mt-2 text-[12.5px] leading-snug text-muted">
          {MATERIALS_HELP}
        </p>
      </div>
      <AttachmentChips
        files={queue.files}
        errors={queue.errors}
        disabled={disabled}
        onRemove={queue.remove}
        errorId={`${id}-error`}
        listClassName="mt-2.5"
        errorClassName="mt-2"
        leading={kept.map((m) => (
          <AttachmentChip
            key={m.id}
            name={m.name}
            tile={<FileGlyph attachment={m} className="text-accent" />}
            meta="Read earlier"
            disabled={disabled || !onRemoveKept}
            onRemove={() => onRemoveKept?.(m.id)}
          />
        ))}
      />
      {note && <p className="mt-2 text-[12.5px] leading-snug text-muted">{note}</p>}
    </div>
  )
}
