import { useCallback, useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import { ApiError } from '../api/client'
import {
  createAssistantConversation,
  fetchAssistantCapabilities,
  sendAssistantMessage,
  settleAssistantConfirmation,
} from '../api/endpoints'
import type { AssistantConsequence, AssistantEvent } from '../api/types'
import { keys } from '../data/keys'

/**
 * The conversation as the panel shows it.
 *
 * ONE HOOK OWNS THE TURN. The server streams a turn as events; this hook
 * folds them into a list of things a person can read -- their own message,
 * the assistant's reply as it arrives, a chip per tool the assistant used, a
 * confirmation card when it needs approval, a hand-off card when the console
 * itself has to finish the job -- and it is the only thing that talks to the
 * assistant endpoints.
 *
 * WHEN A TOOL CHANGES SOMETHING, THE SCREENS BEHIND THE PANEL REFRESH. The
 * same rule as every other write in this console (see data/console.ts): a
 * person added, a rule granted or removed, an enrolment started -- the
 * caches those screens read are invalidated the moment the tool result says
 * it ran, so closing the panel lands on the new state, not the old one.
 */

export type ChatItem =
  | { kind: 'user'; id: string; text: string }
  | { kind: 'assistant'; id: string; text: string; streaming: boolean }
  | { kind: 'tool'; id: string; tool: string; summary: string; status: string; pending: boolean }
  | {
      kind: 'confirmation'
      id: string
      token: string
      tool: string
      consequence: AssistantConsequence
      phraseRequired: string
      expiresAt: string
      settled: 'approved' | 'rejected' | null
    }
  | { kind: 'handoff'; id: string; label: string; route: string; handoffKind: string }
  | { kind: 'failure'; id: string; code: string; message: string; retryable: boolean }

/** Tools whose success changes what other screens show. */
const WRITE_TOOLS = new Set(['create_person', 'grant_access', 'revoke_access', 'start_enrollment'])

const TOOL_LABELS: Record<string, string> = {
  search_people: 'Searched people',
  get_person: 'Looked up a person',
  get_person_enrollment: 'Checked an enrolment',
  list_terminals: 'Listed terminals',
  get_terminal: 'Looked up a terminal',
  get_fleet_summary: 'Checked terminal health',
  list_sites: 'Listed sites',
  get_site: 'Looked up a site',
  list_schedules: 'Listed schedules',
  list_events: 'Read recent events',
  create_person: 'Added a person',
  grant_access: 'Access rule',
  revoke_access: 'Access rule',
  start_enrollment: 'Fingerprint enrolment',
  wait_for_enrollment: 'Waited for the enrolment',
}

export function describeTool(tool: string): string {
  return TOOL_LABELS[tool] ?? tool.replaceAll('_', ' ')
}

export function useAssistantCapabilities() {
  return useQuery({
    queryKey: keys.assistant.capabilities(),
    queryFn: fetchAssistantCapabilities,
    // A deployment flag, not live data.
    staleTime: 5 * 60_000,
    retry: false,
  })
}

let nextId = 0
function makeId(prefix: string): string {
  nextId += 1
  return `${prefix}-${nextId}`
}

export function useAssistantChat() {
  const queryClient = useQueryClient()
  const [conversationId, setConversationId] = useState<string | null>(null)
  const [items, setItems] = useState<ChatItem[]>([])
  const [busy, setBusy] = useState(false)
  const abort = useRef<AbortController | null>(null)
  // What each tool call was, so a later result can say what changed.
  const calls = useRef<Map<string, string>>(new Map())

  useEffect(() => () => abort.current?.abort(), [])

  const patch = useCallback((id: string, update: (item: ChatItem) => ChatItem) => {
    setItems((current) => current.map((item) => (item.id === id ? update(item) : item)))
  }, [])

  const refreshAfter = useCallback(
    (tool: string) => {
      if (!WRITE_TOOLS.has(tool)) return
      void queryClient.invalidateQueries({ queryKey: keys.people.all })
      void queryClient.invalidateQueries({ queryKey: keys.permissions.all })
      void queryClient.invalidateQueries({ queryKey: keys.schedules.all })
      void queryClient.invalidateQueries({ queryKey: keys.onboarding.all })
      void queryClient.invalidateQueries({ queryKey: keys.audit.all })
    },
    [queryClient],
  )

  const applyEvent = useCallback(
    (event: AssistantEvent, assistantItemId: { current: string | null }) => {
      switch (event.type) {
        case 'assistant.delta': {
          if (!assistantItemId.current) {
            const id = makeId('assistant')
            assistantItemId.current = id
            setItems((current) => [...current, { kind: 'assistant', id, text: event.text, streaming: true }])
          } else {
            patch(assistantItemId.current, (item) =>
              item.kind === 'assistant' ? { ...item, text: item.text + event.text } : item,
            )
          }
          break
        }
        case 'assistant.message': {
          // The completed text replaces whatever streamed, so a reconnect or
          // a replay renders the same words as a live stream.
          if (assistantItemId.current) {
            patch(assistantItemId.current, (item) =>
              item.kind === 'assistant' ? { ...item, text: event.text, streaming: false } : item,
            )
          } else {
            setItems((current) => [
              ...current,
              { kind: 'assistant', id: makeId('assistant'), text: event.text, streaming: false },
            ])
          }
          assistantItemId.current = null
          break
        }
        case 'tool.call': {
          calls.current.set(event.call_id, event.tool)
          setItems((current) => [
            ...current,
            { kind: 'tool', id: `tool-${event.call_id}`, tool: event.tool, summary: 'working…', status: '', pending: true },
          ])
          assistantItemId.current = null
          break
        }
        case 'tool.result': {
          const tool = event.tool ?? calls.current.get(event.call_id) ?? 'tool'
          const id = `tool-${event.call_id}`
          setItems((current) =>
            current.some((item) => item.id === id)
              ? current.map((item) =>
                  item.id === id && item.kind === 'tool'
                    ? { ...item, summary: event.summary, status: event.status, pending: false }
                    : item,
                )
              : [...current, { kind: 'tool', id, tool, summary: event.summary, status: event.status, pending: false }],
          )
          if (event.status === 'EXECUTED' || event.status === 'CONFIRMED_EXECUTED') {
            refreshAfter(tool)
          }
          break
        }
        case 'handoff': {
          setItems((current) => [
            ...current,
            { kind: 'handoff', id: makeId('handoff'), label: event.label, route: event.route, handoffKind: event.kind },
          ])
          break
        }
        case 'confirmation.required': {
          setItems((current) => [
            ...current,
            {
              kind: 'confirmation',
              id: `confirm-${event.confirmation_id}`,
              token: event.token,
              tool: event.tool,
              consequence: event.consequence,
              phraseRequired: event.phrase_required,
              expiresAt: event.expires_at,
              settled: null,
            },
          ])
          break
        }
        case 'turn.failed': {
          if (assistantItemId.current) {
            patch(assistantItemId.current, (item) =>
              item.kind === 'assistant' ? { ...item, streaming: false } : item,
            )
            assistantItemId.current = null
          }
          setItems((current) => [
            ...current,
            { kind: 'failure', id: makeId('failure'), code: event.code, message: event.message, retryable: event.retryable },
          ])
          break
        }
        case 'turn.started':
        case 'turn.completed':
          break
      }
    },
    [patch, refreshAfter],
  )

  const runTurn = useCallback(
    async (start: (id: string, onEvent: (event: AssistantEvent) => void, signal: AbortSignal) => Promise<void>) => {
      setBusy(true)
      const controller = new AbortController()
      abort.current = controller
      const assistantItemId = { current: null as string | null }
      try {
        let id = conversationId
        if (!id) {
          const created = await createAssistantConversation()
          id = created.id
          setConversationId(id)
        }
        await start(id, (event) => applyEvent(event, assistantItemId), controller.signal)
      } catch (error) {
        if (controller.signal.aborted) return
        const message =
          error instanceof ApiError ? error.message : 'The assistant could not be reached.'
        const code = error instanceof ApiError && error.code ? error.code : 'request_failed'
        setItems((current) => [
          ...current,
          { kind: 'failure', id: makeId('failure'), code, message, retryable: true },
        ])
      } finally {
        if (abort.current === controller) abort.current = null
        setBusy(false)
      }
    },
    [applyEvent, conversationId],
  )

  const send = useCallback(
    async (text: string) => {
      const trimmed = text.trim()
      if (!trimmed || busy) return
      setItems((current) => [...current, { kind: 'user', id: makeId('user'), text: trimmed }])
      const clientMessageId = makeId('cm')
      await runTurn((id, onEvent, signal) => sendAssistantMessage(id, trimmed, clientMessageId, onEvent, signal))
    },
    [busy, runTurn],
  )

  const settle = useCallback(
    async (itemId: string, token: string, approve: boolean, phrase?: string) => {
      if (busy) return
      patch(itemId, (item) =>
        item.kind === 'confirmation' ? { ...item, settled: approve ? 'approved' : 'rejected' } : item,
      )
      await runTurn((id, onEvent, signal) =>
        settleAssistantConfirmation(id, token, approve, phrase, onEvent, signal),
      )
    },
    [busy, patch, runTurn],
  )

  const reset = useCallback(() => {
    abort.current?.abort()
    abort.current = null
    setConversationId(null)
    setItems([])
    setBusy(false)
    calls.current.clear()
  }, [])

  return { conversationId, items, busy, send, settle, reset }
}
