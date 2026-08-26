import { useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router-dom'

import type { AuditQuery, AuditRecord } from '../../api/types'
import { roleLabel } from '../../auth/roles'
import { Badge } from '../../components/Badge'
import { DataTable, type Column } from '../../components/DataTable'
import { SelectField, TextField } from '../../components/Form'
import { Pagination, SearchInput } from '../../components/Pagination'
import { InfoNote, PageHeader, RefreshingIndicator } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { useAuditEvents } from '../../data/console'
import {
  describeAction,
  describeTarget,
  filterableActions,
  filterableTargets,
  isKnownAction,
  readChanges,
} from './auditVocabulary'

const PAGE_SIZE = 50

/**
 * The operator audit trail.
 *
 * SEC-07: nothing recorded what an operator did. The trail now exists and this
 * is the screen over it — who, when, what, to what, from where, and what
 * changed.
 *
 * WHAT THIS PAGE IS NOT, AND SAYS SO. It is the record of what OPERATORS did in
 * this console. What happened at a door — presentations, grants, denials,
 * enrolments, tamper — is a different stream with a different shape, a different
 * endpoint and a different role, and it lives on Events. The two are kept apart
 * because they answer different questions: "why could she not get in" and "who
 * revoked that terminal" want different screens, and merging them would serve
 * neither. Each page links to the other, once, so nobody concludes the trail is
 * empty when they are simply on the wrong one.
 *
 * FILTERING IS SERVER-SIDE, without exception. Filtering a fetched page in the
 * browser filters the page rather than the trail — silently wrong the moment a
 * company generates more records than fit on one, and silently right in every
 * test with three fixtures. On an audit surface that failure mode is worse than
 * anywhere else in the product: it produces a confident, complete-looking answer
 * to "did anybody touch this" that is false.
 *
 * A RECORD IS NEVER HIDDEN FOR BEING UNRECOGNISED. The action column is
 * deliberately unconstrained server-side, so anything this build cannot describe
 * is humanised, marked, and shown.
 */
export function ActivityPage() {
  const [action, setAction] = useState('')
  const [targetType, setTargetType] = useState('')
  const [actor, setActor] = useState('')
  const [since, setSince] = useState('')
  const [until, setUntil] = useState('')
  const [offset, setOffset] = useState(0)
  const [expanded, setExpanded] = useState<string | null>(null)

  /*
    Dates are collected as plain days and widened to instants: `since` from the
    start of that day, `until` to the END of it. Sending a bare date as `until`
    would mean midnight, which silently excludes everything that happened on the
    day the operator asked about -- the single most common date-filter bug, and
    an unusually bad one here because it looks like "nothing happened".

    Local time, deliberately: the operator typed a day as they experience it.
  */
  const query = useMemo<AuditQuery>(
    () => ({
      action: action || undefined,
      target_type: targetType || undefined,
      actor: actor.trim() || undefined,
      since: since ? startOfDay(since) : undefined,
      until: until ? endOfDay(until) : undefined,
      limit: PAGE_SIZE,
      offset,
    }),
    [action, targetType, actor, since, until, offset],
  )

  const audit = useAuditEvents(query)

  const filtered = Boolean(action || targetType || actor.trim() || since || until)

  function change<T>(setter: (value: T) => void) {
    return (value: T) => {
      // Any filter change returns to the first page. Staying on page 4 of a
      // result set that now has one page shows an empty table over a filter that
      // matched plenty.
      setOffset(0)
      setter(value)
    }
  }

  function clearFilters() {
    setAction('')
    setTargetType('')
    setActor('')
    setSince('')
    setUntil('')
    setOffset(0)
  }

  const columns: Column<AuditRecord>[] = [
    {
      id: 'occurred',
      header: 'When',
      primary: true,
      /*
        BOTH TIMES, AND ON AN AUDIT SCREEN THAT IS NOT A LUXURY.

        This showed the relative form only, with the instant tucked into the
        element's `title`. Relative time is right for scanning -- "2 weeks ago"
        answers "is this recent" instantly -- but the question an audit trail is
        opened to answer is usually the other one: what time did this happen, so
        it can be lined up against an incident, a shift, or another system's log.
        A `title` is no answer at all on a touch screen, which has no hover.
      */
      render: (entry) => (
        <span className="audit__when">
          <Timestamp value={entry.occurred_at} relative />
          <span className="audit__when-absolute">
            <Timestamp value={entry.occurred_at} />
          </span>
        </span>
      ),
    },
    {
      id: 'action',
      header: 'What happened',
      render: (entry) => {
        const definition = describeAction(entry.action)
        return (
          <span className="badge-group">
            <Badge tone={definition.tone === 'destructive' ? 'danger' : definition.tone === 'notable' ? 'info' : 'neutral'}>
              {definition.label}
            </Badge>
            {/* Marked rather than dropped: the server's action column is open,
                and an application may define events this build predates. */}
            {!isKnownAction(entry.action) ? (
              <code className="mono badge__note">{entry.action}</code>
            ) : null}
          </span>
        )
      },
    },
    {
      id: 'actor',
      header: 'Who',
      render: (entry) => (
        <span className="audit__actor">
          {entry.actor_email ? (
            <span className="mono">{entry.actor_email}</span>
          ) : (
            <span className="muted">Not recorded</span>
          )}
          {/*
            PLATFORM is not one of the operator roles. It marks a change made by
            the vendor's platform-administration surface rather than by somebody
            inside this company, and a reader of the trail has to be able to tell
            -- reusing an operator role for it would be a lie nobody could detect
            afterwards.
          */}
          {entry.actor_role === 'PLATFORM' ? (
            <Badge tone="warning">Platform</Badge>
          ) : entry.actor_role ? (
            /*
              `roleLabel`, the same function the operators list and every role
              badge use. This column printed the stored enum -- "ADMIN" -- beside
              an email address, while the Operators screen called the same value
              "Administrator". One product, two vocabularies for one fact.

              PLATFORM is deliberately NOT passed through it: it is not an
              operator role, it marks a change made by the vendor's own surface,
              and `roleLabel` would have nothing to say about it. Its badge is
              unchanged.
            */
            <span className="audit__role">{roleLabel(entry.actor_role)}</span>
          ) : null}
        </span>
      ),
    },
    {
      id: 'target',
      header: 'What it was done to',
      render: (entry) =>
        entry.target_label || entry.target_id ? (
          <span className="audit__target">
            {/*
              Humanised, because the filter above this table already offers these
              same values as "Terminal" and "Site" while the column printed
              "TERMINAL" and "SITE". An operator filtering by one and reading the
              other had to work out they were the same set.

              THROUGH `describeTarget` RATHER THAN `humaniseCode`, so the column
              and the filter stay the same set even where the stored word and
              the customer's word differ -- APPLICATION is shown as "Feature" in
              both, which is what every other screen calls it.

              THE TARGET'S OWN IDENTIFIER IS UNTOUCHED and stays monospaced: it
              is the serial, the email or the id that makes the record traceable,
              and it is the half of this column the audit contract depends on.
            */}
            {entry.target_type ? (
              <span className="audit__role">{describeTarget(entry.target_type)}</span>
            ) : null}{' '}
            <span className="mono">{entry.target_label || entry.target_id}</span>
          </span>
        ) : (
          <span className="muted">—</span>
        ),
    },
    {
      id: 'detail',
      header: 'Detail',
      /*
        NOT `secondary`, AND THAT WAS THE WHOLE OF THE MOBILE BUG.

        `secondary` hides a cell below the breakpoint -- correctly, for detail
        that does not earn phone space. This cell is not detail; it is the only
        route to it. Marked secondary, the button measured 0x0 at 390px, was
        removed from the accessibility tree along with its cell, and could not be
        focused: there were zero keyboard-reachable Show controls on a phone, so
        a record's IP address and its changes were desktop-only.

        On a phone this now appears as a labelled "Detail" row on each card,
        which is what every other column does there.
      */
      align: 'end',
      render: (entry) => {
        const changes = readChanges(entry.changes)
        if (changes.length === 0 && !entry.ip_address) {
          return <span className="muted">—</span>
        }
        const open = expanded === entry.id
        return (
          <button
            type="button"
            className="button button--quiet button--small"
            aria-expanded={open}
            onClick={() => setExpanded(open ? null : entry.id)}
          >
            {open ? 'Hide' : 'Show'}
          </button>
        )
      },
    },
  ]

  const page = audit.data

  return (
    <div className="page">
      <PageHeader
        title="Activity"
        lead="What operators have done in this console — who, when, and to what."
        actions={<RefreshingIndicator active={audit.isFetching && !audit.isPending} />}
      />

      {/*
        The sentence that keeps this page honest. Somebody investigating a door
        problem will come here first, and everything they need is elsewhere.
      */}
      {/*
        The two trails are separate on purpose — different endpoint, different
        role, different question — so each page sends people asking the other
        question to the other page, once, rather than leaving them to conclude
        the trail is empty.
      */}
      <InfoNote title="This is the operator trail, not the event log">
        Every record here is a change somebody made in this console. What happened
        at a terminal — who was let in, who was refused and why, enrolments,
        tamper — is in <Link to="/events">Events</Link>.
      </InfoNote>

      <section className="panel" aria-labelledby="activity-filters-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="activity-filters-heading">
            Filters
          </h2>
          <p className="field__hint">
            Applied by the server across the whole trail, not to the page below.
          </p>
        </div>

        <div className="filter-grid">
          <SelectField
            label="Action"
            value={action}
            placeholder="Any action"
            onChange={change(setAction)}
            options={filterableActions()}
          />

          <SelectField
            label="Target type"
            value={targetType}
            placeholder="Anything"
            onChange={change(setTargetType)}
            options={filterableTargets()}
          />

          <SearchInput
            label="Operator"
            value={actor}
            onChange={change(setActor)}
            placeholder="Part of an email address"
            busy={audit.isFetching && actor.trim().length > 0}
          />

          {/*
            ONE GRID CELL FOR BOTH ENDS OF THE RANGE.

            `.filter-grid` is `auto-fit`, so with five filters at 1440px it made
            four columns and dropped "To" onto a row of its own, beside a wide
            empty gap -- the two halves of one range separated, which is the pair
            that most needs to be read together. Wrapping them means the range
            travels as one item at every width and stacks as one on a phone.
          */}
          <div className="filter-range">
            <TextField
              label="From"
              type="date"
              value={since}
              onChange={change(setSince)}
              hint="Includes the whole day."
            />

            <TextField
              label="To"
              type="date"
              value={until}
              onChange={change(setUntil)}
              hint="Includes the whole day."
            />
          </div>
        </div>

        {filtered ? (
          <button type="button" className="button button--quiet" onClick={clearFilters}>
            Clear filters
          </button>
        ) : null}
      </section>

      <DataTable
        caption="Activity"
        columns={columns}
        rows={page?.entries}
        rowKey={(entry) => entry.id}
        isLoading={audit.isPending}
        isFetching={audit.isFetching}
        error={audit.error}
        onRetry={() => void audit.refetch()}
        emptyTitle={filtered ? 'Nothing matched' : 'Nothing recorded yet'}
        emptyDescription={
          filtered
            ? 'No activity matches these filters. Widen the dates or clear them to see everything.'
            : 'No operator has made a change that this console records. Actions are recorded from the moment they happen — this does not include anything done before the audit trail existed.'
        }
      />

      {/*
        The expanded row's detail, rendered BELOW the table rather than as an
        extra row inside it. A row that expands into a second row of a different
        shape breaks the table's column semantics for a screen reader, and the
        responsive card layout has no place to put it at all.
      */}
      {expanded && page ? (
        <ExpandedDetail entry={page.entries.find((e) => e.id === expanded)} />
      ) : null}

      {page ? (
        <Pagination
          count={page.count}
          total={page.total}
          offset={page.offset}
          limit={page.limit}
          hasMore={page.has_more}
          onOffsetChange={setOffset}
          noun="records"
          busy={audit.isFetching}
        />
      ) : null}
    </div>
  )
}

/**
 * One record, opened.
 *
 * IT IS BELOW THE TABLE AND IT HAS TO COME TO YOU.
 *
 * Rendering it here rather than as an expanded row is deliberate and stays: a
 * second row of a different shape breaks the table's column semantics for a
 * screen reader, and the responsive card layout has nowhere to put one. The cost
 * is that "below the table" means below FIFTY ROWS -- measured at 2,882px down
 * at 1440px and 7,887px down at 390px, with the page not scrolling and focus
 * left on the button. Pressing Show did nothing an operator could perceive.
 *
 * So opening a record now brings the reader to it: the panel is scrolled into
 * view and its heading takes focus. That serves both halves of the problem at
 * once -- a pointer user sees the panel, and a keyboard or screen-reader user is
 * placed inside it rather than left forty rows above it.
 *
 * `scrollIntoView` WITHOUT `behavior: 'smooth'`, deliberately: a long animated
 * jump is exactly the motion `prefers-reduced-motion` exists to suppress, and
 * there is nothing to be learned from watching fifty rows go past.
 */
function ExpandedDetail({ entry }: { entry: AuditRecord | undefined }) {
  const headingRef = useRef<HTMLHeadingElement | null>(null)

  // Keyed on the record, so moving from one open record to another moves the
  // reader again rather than leaving them on a panel that quietly changed.
  const entryId = entry?.id
  useEffect(() => {
    if (!entryId) return
    // Optional-called because `scrollIntoView` is not implemented in jsdom, and
    // it is the expendable half: FOCUS is what makes the panel reachable, and
    // moving focus scrolls the target into view in every real browser anyway.
    // The scroll is here for predictable placement, not for reachability.
    headingRef.current?.scrollIntoView?.({ block: 'nearest' })
    headingRef.current?.focus()
  }, [entryId])

  if (!entry) return null
  const changes = readChanges(entry.changes)

  return (
    <section className="panel" aria-label="Record detail">
      <div className="panel__header">
        <h2 className="panel__title" tabIndex={-1} ref={headingRef}>
          {describeAction(entry.action).label}
        </h2>
        <p className="field__hint">
          <Timestamp value={entry.occurred_at} /> · {entry.actor_email ?? 'actor not recorded'}
        </p>
      </div>

      <dl className="detail-list">
        {entry.target_label || entry.target_id ? (
          <div className="detail-list__row">
            <dt>Target</dt>
            <dd>
              <span className="mono">{entry.target_label || entry.target_id}</span>
            </dd>
          </div>
        ) : null}

        {entry.ip_address ? (
          <div className="detail-list__row">
            <dt>From</dt>
            <dd>
              <span className="mono">{entry.ip_address}</span>
            </dd>
          </div>
        ) : null}

        {changes.map((change) => (
          <div className="detail-list__row" key={change.key}>
            <dt>{change.key}</dt>
            <dd>{change.value}</dd>
          </div>
        ))}
      </dl>

      {changes.length === 0 ? (
        <p className="field__hint">
          This action recorded no additional detail. That is normal for changes
          whose meaning is fully carried by the action and its target.
        </p>
      ) : null}
    </section>
  )
}

/** Local start of a plain date, as an instant. */
function startOfDay(date: string): string {
  return new Date(`${date}T00:00:00`).toISOString()
}

/**
 * Local END of a plain date, as an instant.
 *
 * The reason `until` is not simply `${date}T00:00:00`: that would mean midnight
 * at the START of the day, excluding everything the operator asked to see.
 */
function endOfDay(date: string): string {
  return new Date(`${date}T23:59:59.999`).toISOString()
}
