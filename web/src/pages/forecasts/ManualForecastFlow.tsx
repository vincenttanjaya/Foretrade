import type { ForecastDraft } from '../../lib/types'
import { cx } from '../../lib/cx'
import { IconCheck } from '../../components/ui/icons'
import { categoryLabel, fmtDay, shapeLabel } from './forecast'

export const MANUAL_STEPS = [
  {
    label: 'Shape',
    title: 'Choose the shape.',
    description: 'Start with the kind of answer you need: yes or no, a numeric range, or one of several named outcomes.',
  },
  {
    label: 'Ask',
    title: 'Ask a clear question.',
    description: 'Make the question specific. Outcomes should be mutually exclusive and cover every possible result, so exactly one can win.',
  },
  {
    label: 'Resolve',
    title: 'Decide how it resolves.',
    description: 'Name the evidence that will settle the question and when the answer will be known.',
  },
  {
    label: 'Review',
    title: 'Review your forecast.',
    description: 'Check the complete draft before the desk starts its research. You can return to any earlier step to make changes.',
  },
] as const

export function ManualProgress({ step, disabled, onChange }: { step: number; disabled: boolean; onChange: (step: number) => void }) {
  return (
    <nav aria-label="Forecast steps" className="mb-7">
      <ol className="grid grid-cols-4 gap-2 sm:gap-3">
        {MANUAL_STEPS.map((item, i) => (
          <li key={item.label}>
            <button
              type="button"
              aria-current={i === step ? 'step' : undefined}
              disabled={disabled || i > step}
              onClick={() => onChange(i)}
              className={cx(
                'flex w-full flex-col items-center gap-2 rounded-xl border px-2 py-3 text-[12px] font-medium transition-colors sm:flex-row sm:gap-3 sm:px-4 sm:text-sm',
                i === step ? 'border-accent bg-accent-soft/50 text-text' : i < step ? 'border-border bg-surface text-text hover:bg-surface-2' : 'border-border text-muted opacity-60',
              )}
            >
              <span aria-hidden="true" className={cx('grid size-6 shrink-0 place-items-center rounded-full font-mono text-[11px]', i <= step ? 'bg-accent text-[#17120a]' : 'bg-surface-2')}>
                {i < step ? <IconCheck size={12} strokeWidth={2.5} /> : i + 1}
              </span>
              <span>{item.label}</span>
              <span className="sr-only">{i < step ? ' — completed' : i === step ? ' — current' : ' — upcoming'}</span>
            </button>
          </li>
        ))}
      </ol>
    </nav>
  )
}

export function ManualDraftSummary({ draft, materials = [] }: { draft: ForecastDraft; materials?: string[] }) {
  return (
    <>
      <div>
        <div className="label">{shapeLabel(draft.shape)} question</div>
        <h2 className="mt-3 font-display text-[25px] leading-snug break-words text-text">{draft.question.trim()}</h2>
      </div>
      <div>
        <h3 className="label">Outcomes</h3>
        <ol className="mt-3 grid gap-2 sm:grid-cols-2">
          {draft.outcomes.map((outcome, i) => (
            <li key={i} className="flex items-start gap-3 rounded-lg border border-border bg-surface-2 px-3 py-2.5 text-sm text-text">
              <span className="mt-0.5 font-mono text-[11px] text-muted">{i + 1}</span>
              <span className="min-w-0 break-words">{outcome.trim()}</span>
            </li>
          ))}
        </ol>
      </div>
      <div>
        <h3 className="label">Resolution criteria</h3>
        <p className="mt-2 whitespace-pre-wrap text-sm leading-relaxed break-words text-text">{draft.resolutionCriteria.trim()}</p>
      </div>
      <dl className="grid grid-cols-2 gap-4 border-t border-border pt-5">
        <div><dt className="label">Resolution date</dt><dd className="mt-1.5 text-sm text-text">{fmtDay(draft.resolutionDate, true)}</dd></div>
        <div><dt className="label">Category</dt><dd className="mt-1.5 text-sm text-text">{categoryLabel(draft.category)}</dd></div>
      </dl>
      {!!draft.context?.trim() && (
        <div>
          <h3 className="label">Context for the desk</h3>
          <p className="mt-2 whitespace-pre-wrap text-sm leading-relaxed break-words text-muted">{draft.context.trim()}</p>
        </div>
      )}
      {materials.length > 0 && (
        <div>
          <h3 className="label">Materials · {materials.length}</h3>
          <ul className="mt-2 flex flex-col gap-1 text-sm text-text">
            {materials.map((name, i) => (
              <li key={`${i}-${name}`} className="min-w-0 break-words">
                {name}
              </li>
            ))}
          </ul>
          <p className="mt-1.5 text-[12.5px] text-muted">Read once when the run starts; we keep the extracted notes, not the files.</p>
        </div>
      )}
    </>
  )
}
