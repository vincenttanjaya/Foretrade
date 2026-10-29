import { useEffect, useRef, type ComponentProps } from 'react'
import ReactMarkdown, { type Components, type Options } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { cx } from '../../lib/cx'

// Raw HTML in the markdown is not rendered (react-markdown default), so model output stays inert.
const components: Components = {
  a: ({ node: _node, ...props }) => <a {...props} target="_blank" rel="noopener noreferrer" />,
  // Model output can carry prompt-injected web text; a rendered image would fetch an attacker URL (with whatever
  // data it put in the query) with no click, on every reopen. The app never needs model-drawn images.
  img: () => null,
  table: ({ node: _node, ...props }) => <Table {...props} />,
}

/** A horizontally scrolling table; `data-overflow` (a right-edge fade in index.css) marks unseen columns to the right. */
function Table(props: ComponentProps<'table'>) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = ref.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const check = () => el.toggleAttribute('data-overflow', el.scrollWidth - el.clientWidth - el.scrollLeft > 1)
    const observer = new ResizeObserver(check)
    observer.observe(el)
    if (el.firstElementChild) observer.observe(el.firstElementChild) // a column can widen without the box resizing
    el.addEventListener('scroll', check, { passive: true })
    check()
    return () => {
      observer.disconnect()
      el.removeEventListener('scroll', check)
    }
  }, [])
  return (
    <div ref={ref} className="md-table">
      <table {...props} />
    </div>
  )
}

const gfm = [remarkGfm]

/** Styled GitHub-flavoured markdown (tables, lists, code). Styles live in index.css under `.md`. */
export function Markdown({
  children,
  className,
  remarkPlugins = gfm,
}: {
  children: string
  className?: string
  remarkPlugins?: Options['remarkPlugins']
}) {
  return (
    <div className={cx('md', className)}>
      <ReactMarkdown remarkPlugins={remarkPlugins} components={components}>
        {children}
      </ReactMarkdown>
    </div>
  )
}
