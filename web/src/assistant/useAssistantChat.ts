import { useCallback, useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import { ApiError } from '../api/client'
import {
  createAssistantConversation,
  fetchAssistantCapabilities,
  sendAssistantMessage,
  settleAssistantConfirmation,
} from '../api/endpoints'
import type { AssistantCapabilities, AssistantConsequence, AssistantDomain, AssistantEvent } from '../api/types'
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
 * same rule as every other write in this console (see data/console.ts): the
 * caches those screens read are invalidated the moment the tool result says
 * it ran, so closing the panel lands on the new state, not the old one.
 *
 * WHICH CACHES IS THE SERVER'S CALL, NOT A LIST KEPT HERE. Every write tool
 * declares the domains it changes (the registry refuses one that does not),
 * the tool.result event names them, and `domainKeys` maps each to a root in
 * data/keys.ts. A new write on the server refreshes the right screens
 * without this file changing; a hand-written set of tool names here would
 * go stale the day one was added. The capabilities response carries the
 * same map as a fallback for a result that arrives without domains.
 *
 * A CONFIRMATION CARD SETTLES ON THE SERVER'S WORD, NOT ON THE CLICK. Sending
 * an approval marks the card pending; only the confirmation.settled event --
 * ran, failed, or rejected -- moves it on. A token the server refuses (expired,
 * already used, another session) never produces that event, and the card
 * shows the refusal rather than an approval that did not happen.
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
      /** The answer is on its way to the server and nothing has come back yet. */
      pending: boolean
      /** What the server said became of it; null while unanswered or pending. */
      settled: 'approved' | 'rejected' | 'failed' | null
      /** Why it failed, in the server's words, when settled is 'failed'. */
      failure: string | null
    }
  | { kind: 'handoff'; id: string; label: string; route: string; handoffKind: string }
  | { kind: 'failure'; id: string; code: string; message: string; retryable: boolean }

/** The cache root each server-named domain invalidates. One line per domain, all of them. */
const domainKeys: Record<AssistantDomain, readonly unknown[]> = {
  people: keys.people.all,
  permissions: keys.permissions.all,
  schedules: keys.schedules.all,
  onboarding: keys.onboarding.all,
  audit: keys.audit.all,
  terminals: keys.terminals.all,
  sites: keys.sites.all,
  pending_terminals: keys.pendingTerminals.all,
  events: keys.events.all,
}

/**
 * The domains a tool result says it changed, or the capabilities map's word
 * for that tool when the event carries none. Exported for the tests.
 */
export function domainsFor(
  tool: string,
  event: { domains?: AssistantDomain[] },
  capabilities: AssistantCapabilities | undefined,
): AssistantDomain[] {
  if (event.domains && event.domains.length > 0) return event.domains
  return capabilities?.effects?.[tool] ?? []
}

const TOOL_LABELS: Record<string, string> = {
  search_people: 'Searched people',
  get_person: 'Looked up a person',
  get_person_enrollment: 'Checked an enrolment',
  list_person_credentials: 'Checked where a fingerprint is enrolled',
  list_terminals: 'Listed terminals',
  get_terminal: 'Looked up a terminal',
  get_terminal_capabilities: 'Checked what a terminal can do',
  get_fleet_summary: 'Checked terminal health',
  list_sites: 'Listed sites',
  get_site: 'Looked up a site',
  get_site_settings: "Read a site's offline policy",
  list_schedules: 'Listed schedules',
  list_events: 'Read recent events',
  list_people_without_access: 'Counted people with no access',
  evaluate_access: 'Checked whether they would get in',
  explain_denial: 'Looked into a refusal',
  create_person: 'Added a person',
  update_person: 'Corrected a person',
  set_person_active: 'Person active or not',
  grant_access: 'Access rule',
  revoke_access: 'Access rule',
  create_schedule: 'Added a schedule',
  update_schedule: 'Schedule change',
  start_enrollment: 'Fingerprint enrolment',
  wait_for_enrollment: 'Waited for the enrolment',
  cancel_enrollment: 'Cancelled an enrolment',
  list_terminal_commands: "Read a terminal's command history",
  get_command: 'Checked a command',
  request_diagnostic: 'Asked a terminal for a diagnostic',
  wait_for_command: 'Waited for the terminal',
  resync_terminal: 'Resynced a terminal',
  list_pending_terminals: 'Checked terminals waiting to be set up',
  list_audit: 'Read who changed what',
  withdraw_command: 'Cancelled a command',
  run_device_test: 'Hardware test',
  delete_schedule: 'Removed a schedule',
  approve_pending_terminal: 'Terminal set-up',
  reject_pending_terminal: 'Terminal set-up',
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
  const capabilities = useAssistantCapabilities().data
  const [conversationId, setConversationId] = useState<string | null>(null)
  const [items, setItems] = useState<ChatItem[]>([])
  const [busy, setBusy] = useState(false)
  // The conversation reached its bound; only a new one takes a message.
  const [closed, setClosed] = useState(false)
  const abort = useRef<AbortController | null>(null)
  // What each tool call was, so a later result can say what changed.
  const calls = useRef<Map<string, string>>(new Map())
  // The confirmation card whose answer is in flight, so a refusal or a lost
  // stream can be written back to it.
  const settling = useRef<string | null>(null)

  useEffect(() => () => abort.current?.abort(), [])

  const patch = useCallback((id: string, update: (item: ChatItem) => ChatItem) => {
    setItems((current) => current.map((item) => (item.id === id ? update(item) : item)))
  }, [])

  const refreshAfter = useCallback(
    (tool: string, event: { domains?: AssistantDomain[] }) => {
      for (const domain of domainsFor(tool, event, capabilities)) {
        const queryKey = domainKeys[domain]
        if (queryKey) void queryClient.invalidateQueries({ queryKey })
      }
    },
    [capabilities, queryClient],
  )

  const settleCard = useCallback(
    (itemId: string, outcome: 'approved' | 'rejected' | 'failed', failure: string | null) => {
      patch(itemId, (item) =>
        item.kind === 'confirmation' ? { ...item, pending: false, settled: outcome, failure } : item,
      )
    },
    [patch],
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
            refreshAfter(tool, event)
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
              pending: false,
              settled: null,
              failure: null,
            },
          ])
          break
        }
        case 'confirmation.settled': {
          const id = `confirm-${event.confirmation_id}`
          settleCard(id, event.outcome, event.outcome === 'failed' ? (event.message ?? 'It did not run.') : null)
          if (settling.current === id) settling.current = null
          break
        }
        case 'turn.failed': {
          if (event.code === 'conversation_full') setClosed(true)
          if (settling.current) {
            // The server checked the token again under the stream and refused
            // it (a second approval racing this one, say). Nothing ran.
            settleCard(settling.current, 'failed', event.message)
            settling.current = null
          }
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
        case 'turn.completed': {
          if (event.conversation_closed) {
            setClosed(true)
            setItems((current) => [
              ...current,
              {
                kind: 'failure',
                id: makeId('failure'),
                code: 'conversation_full',
                message: 'This conversation has reached its limit. Start a new one to continue.',
                retryable: false,
              },
            ])
          }
          break
        }
        case 'turn.started':
          break
      }
    },
    [patch, refreshAfter, settleCard],
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
        if (code === 'conversation_closed') setClosed(true)
        if (settling.current) {
          // The server refused the answer before anything ran: an expired or
          // spent token, another session's, a malformed one. The card says so.
          settleCard(settling.current, 'failed', message)
          settling.current = null
        }
        setItems((current) => [
          ...current,
          { kind: 'failure', id: makeId('failure'), code, message, retryable: code !== 'conversation_closed' },
        ])
      } finally {
        if (settling.current) {
          // The stream ended without the server saying what became of it.
          // Not approved: nothing confirmed that it ran.
          settleCard(settling.current, 'failed', 'No outcome was reported. Check the audit trail before trying again.')
          settling.current = null
        }
        if (abort.current === controller) abort.current = null
        setBusy(false)
      }
    },
    [applyEvent, conversationId, settleCard],
  )

  const send = useCallback(
    async (text: string) => {
      const trimmed = text.trim()
      if (!trimmed || busy || closed) return
      setItems((current) => [...current, { kind: 'user', id: makeId('user'), text: trimmed }])
      const clientMessageId = makeId('cm')
      await runTurn((id, onEvent, signal) => sendAssistantMessage(id, trimmed, clientMessageId, onEvent, signal))
    },
    [busy, closed, runTurn],
  )

  const settle = useCallback(
    async (itemId: string, token: string, approve: boolean, phrase?: string) => {
      if (busy) return
      // Pending, not settled: what the card shows next is the server's answer.
      settling.current = itemId
      patch(itemId, (item) => (item.kind === 'confirmation' ? { ...item, pending: true } : item))
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
    setClosed(false)
    calls.current.clear()
    settling.current = null
  }, [])

  return { conversationId, items, busy, closed, send, settle, reset }
}
