// The attachment selection shared by the chat composer and the forecast materials field: each picked, pasted or
// dropped file reserves its slot at once and converts on its own (photos to JPEG, documents to base64); an error
// names the file it belongs to.
import { useCallback, useEffect, useRef, useState } from 'react'
import type { ChatAttachmentInput } from '../../lib/types'
import { UNSUPPORTED, classify, prepareAttachment } from '../../pages/agent/attachments'
import { safeImageName } from '../../pages/agent/images'

/** One queued file: `input` is set once it is converted or read, `url` is an image's preview. */
export type Selection = { key: number; name: string; kind: 'image' | 'document'; input?: ChatAttachmentInput; size?: number; url?: string }

export type AttachmentQueue = {
  files: Selection[]
  errors: string[]
  processing: boolean // a file is still converting
  attach: (picked: File[]) => void
  remove: (key: number) => void
  clear: () => void // drops every file (revoking previews) and the errors
  clearErrors: () => void
  inputs: () => ChatAttachmentInput[] // the converted files, in selection order
}

/**
 * `max` is the number of slots (it may shrink while files are queued: the next pick sees it); `per` names the unit in
 * the overflow error ("per message", "per forecast"). Nothing attaches while `disabled`.
 */
export function useAttachmentQueue({ max, disabled = false, per }: { max: number; disabled?: boolean; per: string }): AttachmentQueue {
  const [files, setFiles] = useState<Selection[]>([])
  const [errors, setErrors] = useState<string[]>([])
  const selected = useRef<Selection[]>([])
  const generation = useRef(0)
  const sequence = useRef(0)
  const mounted = useRef(true)

  const update = (next: Selection[]) => {
    selected.current = next
    if (mounted.current) setFiles(next)
  }
  const dropAll = () => {
    generation.current++
    for (const file of selected.current) if (file.url) URL.revokeObjectURL(file.url)
    update([])
  }
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
      dropAll()
    }
  }, [])

  const attach = (picked: File[]) => {
    if (disabled || !picked.length) return
    const found: string[] = []
    const fail = (name: string, message: string) => mounted.current && setErrors((list) => [...list, `${name}: ${message}`])
    const free = max - selected.current.length
    const batch = picked.flatMap((file) => {
      const name = safeImageName(file.name, 'file')
      const kind = classify(file)?.kind
      if (!kind) {
        found.push(`${name}: ${UNSUPPORTED}`)
        return []
      }
      return [{ file, key: ++sequence.current, name, kind }]
    })
    if (batch.length > free)
      found.push(`Too many files: attach at most ${max} ${per}. Left out: ${batch.slice(Math.max(0, free)).map((item) => item.name).join(', ')}.`)
    setErrors(found)
    const accepted = batch.slice(0, Math.max(0, free))
    const version = generation.current
    update([...selected.current, ...accepted.map(({ key, name, kind }) => ({ key, name, kind }))])
    for (const { file, key, name } of accepted) {
      const current = () => mounted.current && generation.current === version && selected.current.some((item) => item.key === key)
      void prepareAttachment(file)
        .then(({ input, size, blob }) => {
          if (!current()) return
          const url = blob ? URL.createObjectURL(blob) : undefined
          update(selected.current.map((item) => (item.key === key ? { ...item, input, size, url } : item)))
        })
        .catch((error) => {
          if (!current()) return
          update(selected.current.filter((item) => item.key !== key))
          fail(name, error instanceof Error ? error.message : "Couldn't read this file.")
        })
    }
  }

  const remove = (key: number) => {
    const file = selected.current.find((item) => item.key === key)
    if (file?.url) URL.revokeObjectURL(file.url)
    update(selected.current.filter((item) => item.key !== key))
    setErrors([])
  }

  const clear = () => {
    dropAll()
    setErrors([])
  }

  return {
    files,
    errors,
    processing: files.some((file) => !file.input),
    attach,
    remove,
    clear,
    clearErrors: useCallback(() => setErrors([]), []),
    inputs: () => selected.current.flatMap((file) => (file.input ? [file.input] : [])),
  }
}
