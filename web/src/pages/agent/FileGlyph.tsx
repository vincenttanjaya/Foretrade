import { IconFile } from '../../components/ui/icons'
import { cx } from '../../lib/cx'
import { attachmentShort } from './attachments'

/** A document's tile in an attachment chip: file outline with its short type inside. Decorative; the chip names the type. */
export function FileGlyph({ attachment, className }: { attachment: { kind: string; mediaType: string }; className?: string }) {
  return (
    <span aria-hidden="true" className={cx('relative grid size-10 shrink-0 place-items-center', className)}>
      <IconFile size={34} strokeWidth={1.3} />
      <span className="absolute inset-x-0 bottom-[11px] text-center font-mono text-[7.5px] leading-none font-bold tracking-tight">{attachmentShort(attachment)}</span>
    </span>
  )
}
