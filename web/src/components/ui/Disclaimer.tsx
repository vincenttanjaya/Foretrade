import { cx } from '../../lib/cx'
import { IconInfo } from './icons'

/** Required on every AI card. */
export function Disclaimer({ className }: { className?: string }) {
  return (
    <p className={cx('inline-flex items-center gap-1.5 font-mono text-[10.5px] tracking-[0.06em] text-muted', className)}>
      <IconInfo size={12} />
      Simulated · not financial advice
    </p>
  )
}
