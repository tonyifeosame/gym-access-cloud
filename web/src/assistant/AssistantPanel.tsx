import { useEffect, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'
import { Link } from 'react-router-dom'

import { Badge } from '../components/Badge'
import { InfoNote } from '../components/states'
import { type ChatItem, describeTool, useAssistantChat } from './useAssistantChat'

/**
 * The assistant, as a panel beside whatever screen is open.
 *
 * WHAT IT IS NOT: a second console. It answers questions and does a handful
 * of everyday things -- find a person, add one, grant or remove access, ask a
 * terminal to capture a fingerprint -- and for everything else it points at
 * the screen that does it. Anything consequential is shown as a card the
 * operator approves or rejects; nothing runs on the assistant's say-so.
 *
 * The panel is a dialog for assistive technology (it takes focus, Escape
 * closes it) but it is NOT modal: the console behind it stays usable, because
 * the point of a hand-off card is to click through to the screen it names.
 */
export function AssistantPanel({ open, onClose }: { open: boolean; onClose: () => void }) {
  const chat = useAssistantChat()
  const [draft, setDraft] = useState('')
  const listRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    if (open) inputRef.current?.focus()
  }, [open])

  // Follow the conversation as it grows. (jsdom has no scrollTo.)
  useEffect(() => {
    const list = listRef.current
    if (list && typeof list.scrollTo === 'function') {
      list.scrollTo({ top: list.scrollHeight })
    }
  }, [chat.items])

  if (!open) return null

  async function submit(event?: FormEvent) {
    event?.preventDefault()
    const text = draft
    setDraft('')
    await chat.send(text)
  }

  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault()
      void submit()
    }
  }

  return (
    // A <section> with the dialog role, not an <aside>: `aside` is a landmark
    // of its own (complementary), and ARIA does not allow re-roling it.
    <section
      className="assistant"
      role="dialog"
      aria-label="Assistant"
      onKeyDown={(event) => {
        if (event.key === 'Escape') onClose()
      }}
    >
      <div className="assistant__header">
        <h2 className="assistant__title">Assistant</h2>
        <div className="assistant__header-actions">
          <button
            type="button"
            className="button button--quiet button--small"
            onClick={() => {
              chat.reset()
              inputRef.current?.focus()
            }}
            disabled={chat.items.length === 0 && !chat.busy && !chat.closed}
          >
            New conversation
          </button>
          <button type="button" className="button button--quiet button--small" onClick={onClose} aria-label="Close the assistant">
            Close
          </button>
        </div>
      </div>

      <div className="assistant__list" ref={listRef} aria-live="polite" aria-relevant="additions text">
        {chat.items.length === 0 ? (
          <p className="assistant__intro">
            Ask about people, terminals, sites, schedules or recent events, or ask me to add a
            person or change who may get in. Anything that changes access is shown to you to
            approve first.
          </p>
        ) : null}
        {chat.items.map((item) => (
          <ChatEntry key={item.id} item={item} busy={chat.busy} onSettle={chat.settle} />
        ))}
        {chat.busy ? (
          <p className="assistant__status" role="status">
            Working…
          </p>
        ) : null}
      </div>

      <form className="assistant__composer" onSubmit={(event) => void submit(event)}>
        <label className="assistant__label" htmlFor="assistant-input">
          Message the assistant
        </label>
        <textarea
          id="assistant-input"
          ref={inputRef}
          className="assistant__input"
          rows={2}
          maxLength={4000}
          value={draft}
          placeholder={
            chat.closed
              ? 'This conversation is full. Start a new one.'
              : 'e.g. Who was refused at Reception this morning?'
          }
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={onKeyDown}
          disabled={chat.busy || chat.closed}
        />
        <button
          type="submit"
          className="button button--primary"
          disabled={chat.busy || chat.closed || draft.trim() === ''}
        >
          Send
        </button>
      </form>
    </section>
  )
}

function ChatEntry({
  item,
  busy,
  onSettle,
}: {
  item: ChatItem
  busy: boolean
  onSettle: (itemId: string, token: string, approve: boolean, phrase?: string) => Promise<void>
}) {
  switch (item.kind) {
    case 'user':
      return (
        <div className="assistant__entry assistant__entry--user">
          <p className="assistant__bubble assistant__bubble--user">{item.text}</p>
        </div>
      )
    case 'assistant':
      return (
        <div className="assistant__entry">
          <p className="assistant__bubble">
            {item.text}
            {item.streaming ? <span className="assistant__cursor" aria-hidden="true" /> : null}
          </p>
        </div>
      )
    case 'tool':
      return (
        <p className="assistant__tool">
          <span className="assistant__tool-name">{describeTool(item.tool)}</span>
          {item.pending ? (
            <Badge tone="info">working…</Badge>
          ) : (
            <Badge tone={toolTone(item.status)}>{item.summary}</Badge>
          )}
        </p>
      )
    case 'confirmation':
      return <ConfirmationCard item={item} busy={busy} onSettle={onSettle} />
    case 'handoff':
      return (
        <div className="assistant__card">
          <p className="assistant__card-body">The console takes it from here.</p>
          <Link to={item.route} className="button button--primary">
            {item.label}
          </Link>
        </div>
      )
    case 'failure':
      return (
        <InfoNote tone="warning" title={failureTitle(item.code)}>
          {item.message}
        </InfoNote>
      )
  }
}

function settledTone(settled: 'approved' | 'rejected' | 'failed' | null): 'positive' | 'neutral' | 'danger' {
  switch (settled) {
    case 'approved':
      return 'positive'
    case 'failed':
      return 'danger'
    default:
      return 'neutral'
  }
}

function settledLabel(settled: 'approved' | 'rejected' | 'failed' | null): string {
  switch (settled) {
    case 'approved':
      return 'Approved'
    case 'rejected':
      return 'Rejected'
    case 'failed':
      return 'Not done'
    default:
      return ''
  }
}

function toolTone(status: string): 'positive' | 'warning' | 'neutral' | 'danger' {
  switch (status) {
    case 'EXECUTED':
    case 'CONFIRMED_EXECUTED':
      return 'positive'
    case 'CONFIRMATION_REQUESTED':
      return 'neutral'
    case 'REFUSED_ROLE':
    case 'REFUSED_SCOPE':
    case 'FAILED':
      return 'danger'
    default:
      return 'warning'
  }
}

function failureTitle(code: string): string {
  switch (code) {
    case 'session_expired':
      return 'Your session has ended'
    case 'conversation_full':
    case 'conversation_closed':
      return 'Start a new conversation'
    case 'budget_exhausted':
      return 'Assistant allowance used up'
    case 'rate_limited':
      return 'Too many messages'
    case 'tool_limit':
      return 'The assistant stopped'
    case 'timeout':
      return 'That took too long'
    case 'refused':
      return 'The assistant declined'
    default:
      return 'The assistant hit a problem'
  }
}

/**
 * The approval card.
 *
 * THE WORDING IS THE SERVER'S. This card holds no consequence text of its
 * own: the title, the body and the warnings come from the assistant service,
 * which keeps them in one place with the console's own dialogs' wording. What
 * the card owns is the choice -- Approve or Reject -- and the typed phrase
 * where the equivalent screen demands one.
 */
function ConfirmationCard({
  item,
  busy,
  onSettle,
}: {
  item: Extract<ChatItem, { kind: 'confirmation' }>
  busy: boolean
  onSettle: (itemId: string, token: string, approve: boolean, phrase?: string) => Promise<void>
}) {
  const [phrase, setPhrase] = useState('')
  const settled = item.settled !== null
  const phraseOk = item.phraseRequired === '' || phrase.trim() === item.phraseRequired
  const locked = busy || item.pending

  return (
    <section className="assistant__card assistant__card--confirm" aria-labelledby={`${item.id}-title`}>
      <h3 className="assistant__card-title" id={`${item.id}-title`}>
        {item.consequence.title}
      </h3>
      <p className="assistant__card-body">{item.consequence.body}</p>
      {item.consequence.warnings?.map((warning) => (
        <p key={warning} className="assistant__card-warning">
          {warning}
        </p>
      ))}

      {item.phraseRequired && !settled ? (
        <label className="field">
          <span className="field__label">
            Type <code className="mono">{item.phraseRequired}</code> to confirm
          </span>
          <input
            className="field__input"
            value={phrase}
            onChange={(event) => setPhrase(event.target.value)}
            autoComplete="off"
          />
        </label>
      ) : null}

      {settled ? (
        <p className="assistant__card-settled" role="status">
          <Badge tone={settledTone(item.settled)}>{settledLabel(item.settled)}</Badge>
          {item.settled === 'failed' && item.failure ? (
            <span className="assistant__card-failure">{item.failure}</span>
          ) : null}
        </p>
      ) : item.pending ? (
        <p className="assistant__card-settled" role="status">
          <Badge tone="info">Sending…</Badge>
        </p>
      ) : (
        <div className="assistant__card-actions">
          <button
            type="button"
            className="button"
            disabled={locked}
            onClick={() => void onSettle(item.id, item.token, false)}
          >
            Reject
          </button>
          <button
            type="button"
            className="button button--primary"
            disabled={locked || !phraseOk}
            onClick={() => void onSettle(item.id, item.token, true, phrase)}
          >
            Approve
          </button>
        </div>
      )}
    </section>
  )
}
