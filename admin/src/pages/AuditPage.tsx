import { Link, useSearchParams } from 'react-router'
import { Page, PageHeader } from '../components/layout/AdminShell'
import { Button } from '../components/ui/Button'
import { Card } from '../components/ui/Card'
import { EmptyState, ErrorState } from '../components/ui/EmptyState'
import { FormError } from '../components/ui/Modal'
import { Skeleton } from '../components/ui/Spinner'
import { api } from '../lib/api'
import { auditLabel, auditSummary } from '../lib/audit'
import { fmtDate, fmtNum } from '../lib/format'
import { useLoad } from '../lib/useLoad'

const LIMIT = 50

export function AuditPage() {
  const [params, setParams] = useSearchParams()
  const n = Number(params.get('page'))
  const page = Number.isInteger(n) && n > 1 ? n : 1
  const { data, error, loading, reload } = useLoad((signal) => api.audit({ page, limit: LIMIT }, signal), [page])
  const pages = data ? Math.max(1, Math.ceil(data.total / data.limit)) : 1
  const go = (p: number) => setParams(p > 1 ? { page: String(p) } : {})

  return (
    <Page>
      <PageHeader
        label="Audit log"
        title="Every admin change."
        subtitle="Who changed which account, newest first. Entries outlive deleted accounts."
      />
      {error && !data ? (
        <ErrorState message={error} onRetry={reload} />
      ) : (
        <Card className="overflow-hidden">
          {error && <FormError>{error}</FormError>}
          <div className="overflow-x-auto" aria-busy={loading || undefined}>
            <table className="data-table">
              <caption className="sr-only">Audit log, page {page}</caption>
              <thead>
                <tr>
                  <th scope="col">Time</th>
                  <th scope="col">Admin</th>
                  <th scope="col">Action</th>
                  <th scope="col">Account</th>
                  <th scope="col">Detail</th>
                </tr>
              </thead>
              <tbody>
                {data
                  ? data.entries.map((e) => (
                      <tr key={e.id}>
                        <td className="whitespace-nowrap">{fmtDate(e.createdAt, { time: true })}</td>
                        <td className="max-w-56 truncate" title={e.admin.email}>
                          {e.admin.email}
                        </td>
                        <td className="whitespace-nowrap font-medium">{auditLabel(e)}</td>
                        <td className="max-w-56 truncate">
                          {e.target ? (
                            <Link to={`/users/${e.target.id}`} className="text-accent-ink hover:underline" title={e.target.email}>
                              {e.target.email}
                            </Link>
                          ) : (
                            <span className="text-muted">—</span>
                          )}
                        </td>
                        <td className="min-w-48 text-muted">{auditSummary(e) || '—'}</td>
                      </tr>
                    ))
                  : [0, 1, 2, 3, 4].map((i) => (
                      <tr key={i}>
                        <td colSpan={5}>
                          <Skeleton className="h-5 w-full" />
                        </td>
                      </tr>
                    ))}
              </tbody>
            </table>
          </div>
          {data && !data.entries.length && (
            <EmptyState
              className="m-5"
              title={data.total ? 'Nothing on this page.' : 'No admin changes yet.'}
              description={data.total ? undefined : 'Creating, editing, disabling or deleting an account adds an entry here.'}
              action={
                data.total ? (
                  <Button variant="secondary" onClick={() => go(1)}>
                    Go to page 1
                  </Button>
                ) : undefined
              }
            />
          )}
          {data && data.total > 0 && (
            <nav aria-label="Pagination" className="flex flex-wrap items-center justify-between gap-3 border-t border-border px-5 py-3">
              <span className="text-[13px] text-muted tabular-nums">
                Page {fmtNum(page)} of {fmtNum(pages)} · {fmtNum(data.total)} entries
              </span>
              <div className="flex gap-2">
                <Button variant="secondary" size="sm" disabled={page <= 1} onClick={() => go(page - 1)}>
                  Previous
                </Button>
                <Button variant="secondary" size="sm" disabled={page >= pages} onClick={() => go(page + 1)}>
                  Next
                </Button>
              </div>
            </nav>
          )}
        </Card>
      )}
    </Page>
  )
}
