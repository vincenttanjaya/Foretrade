import { useEffect, useRef, useState } from 'react'
import { useParams } from 'react-router'
import { Button } from '../../components/ui/Button'
import { Card } from '../../components/ui/Card'
import { useAuth } from '../../lib/auth'
import { errorMessage } from '../../lib/errors'
import type { MemoryProposal } from '../../lib/types'

export function MemoryCard({ data, durable }: { data: MemoryProposal; durable: boolean }) {
  const { user, confirmMemory, memoryReceipts, reconcileMemory, observeConfirmedMemory } = useAuth()
  const { chatId } = useParams()
  const [pending, setPending] = useState(false)
  const busy = useRef(false)
  const [error, setError] = useState('')
  const receipt = memoryReceipts[data.proposalId]
  // A durable deleted card always wins over an older cached open/confirmed view.
  const status = data.status === 'deleted' || receipt?.status === 'deleted' ? 'deleted' : data.status === 'confirmed' || receipt?.status === 'confirmed' ? 'confirmed' : 'open'
  useEffect(() => {
    if (user && durable) observeConfirmedMemory(user.id, data)
  }, [user, durable, data, observeConfirmedMemory])

  async function confirm() {
    if (busy.current || !durable || !user || status !== 'open') return
    busy.current = true
    setPending(true)
    setError('')
    try { await confirmMemory({ proposalId: data.proposalId, text: data.text }) }
    catch (err) {
      const restored = chatId ? await reconcileMemory(data.proposalId, chatId).catch(() => null) : null
      if (!restored || restored.status === 'open') setError(errorMessage(err, 'Could not save this memory. Please try again.'))
    } finally { busy.current = false; setPending(false) }
  }

  return <Card className="overflow-hidden p-4">
    <div className="label">Memory suggestion</div>
    <p className="mt-2 text-sm leading-relaxed [overflow-wrap:anywhere] whitespace-pre-wrap">{data.text}</p>
    <p className="mt-2 text-[12px] text-muted">Saved only when you confirm. Manage saved memories in your Account.</p>
    {status === 'open' ? <div className="mt-3">
      <Button size="sm" onClick={confirm} loading={pending} disabled={!durable || !user}>Confirm memory</Button>
      {!durable && <p className="mt-2 text-[12px] text-muted">Available when this answer is finished and saved.</p>}
    </div> : <p role="status" className="mt-3 text-sm font-medium">{status === 'confirmed' ? 'Memory saved' : 'Memory deleted'}</p>}
    {status === 'open' && error && <p role="alert" className="mt-2 text-[13px] text-down">{error}</p>}
  </Card>
}
