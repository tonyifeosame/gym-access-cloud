import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'

import type { EventDecision, EventQuery, FieldEvent } from '../../api/types'
import { describeApplication } from '../../applications/registry'
import { Badge, humaniseCode } from '../../components/Badge'
import { DataTable, type Column } from '../../components/DataTable'
import { SelectField, TextField } from '../../components/Form'
import { Pagination, SearchInput } from '../../components/Pagination'
import { InfoNote, PageHeader, RefreshingIndicator } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { useEvents, useSites } from '../../data/console'
import {
  UNCONFIRMED_TIME_LABEL,
  describeEventCause,
  describeReason,
} from '../access/accessVocabulary'
import { PERSON_ID_LABEL } from '../people/personVocabulary'

const PAGE_SIZE = 50

/**
 * What happened at the doors.
 *
 * SEC-08: the only access-log route was authenticated with the site
 * provisioning key, which a browser must never hold — so an operator could not
 * see who had been let in, who had been refused, or why. This is that history.
 *
 * SEPARATE FROM ACTIVITY, deliberately, and the split is not cosmetic. An event
 * trail says what happened in the field; an audit trail names which operators
 * changed what. They have different endpoints, different roles (this is VIEWER,
 * audit is ADMIN), different retention, and — most importantly — different
 * questions. Somebody asking "why could she not get in" and somebody asking "who
 * revoked that terminal" want different screens, and merging them would serve
 * neither.
 *
 * THE DENIALS ARE THE VALUABLE HALF, and the filter defaults reflect it. A grant
 * is a door opening; a denial is somebody standing outside who expected not to
 * be. The reason is rendered as an explanation and a remedy rather than as a
 * code, because "OUTSIDE_SCHEDULE" and "NO_PERMISSION" send an operator to two
 * completely different places and neither is guessable from the string.
 *
 * A PRESENTATION THAT MATCHED NOBODY still appears, and that is the case worth
 * protecting: `person_id` is empty and `subject_external_id` keeps what the
 * terminal actually read, so an unrecognised credential at 3am is visible rather
 * than filtered out for having no person to attach to.
 */
export function EventsPage() {
  const [decision, setDecision] = useState<EventDecision | ''>('')
  const [eventType, setEventType] = useState('')
  const [siteId, setSiteId] = useState('')
  const [serial, setSerial] = useState('')
  const [search, setSearch] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [offset, setOffset] = useState(0)

  const sites = useSites()

  const query = useMemo<EventQuery>(
    () => ({
      decision: decision || undefined,
      event_type: eventType || undefined,
      site_id: siteId || undefined,
      serial: serial.trim() || undefined,
      q: search.trim() || undefined,
      from: from ? new Date(`${from}T00:00:00`).toISOString() : undefined,
      // The END of the day. A bare date means midnight at its start, which
      // silently excludes everything that happened on the day being asked about
      // — and the answer looks like "nothing happened".
      to: to ? new Date(`${to}T23:59:59.999`).toISOString() : undefined,
      limit: PAGE_SIZE,
      offset,
    }),
    [decision, eventType, siteId, serial, search, from, to, offset],
  )

  const events = useEvents(query)
  const filtered = Boolean(decision || eventType || siteId || serial.trim() || search.trim() || from || to)

  function change<T>(setter: (value: T) => void) {
    return (value: T) => {
      setOffset(0)
      setter(value)
    }
  }

  function clearFilters() {
    setDecision('')
    setEventType('')
    setSiteId('')
    setSerial('')
    setSearch('')
    setFrom('')
    setTo('')
    setOffset(0)
  }

  const columns: Column<FieldEvent>[] = [
    {
      id: 'when',
      header: 'When',
      primary: true,
      render: (event) => (
        <span className="event__when">
          <Timestamp value={event.occurred_at} relative />
          {/*
            TWO FACTS THAT DIVERGE. A terminal buffers while offline, so an
            event that arrived just now may have happened hours ago — and a
            terminal that has never reached NTP sends no time at all, in which
            case what is shown is the server's arrival stamp. Presenting either
            as an unqualified door time would be a quiet lie.
          */}
          {!event.occurred_at_trusted ? (
            <Badge tone="warning">{UNCONFIRMED_TIME_LABEL}</Badge>
          ) : null}
          {event.recorded_at !== event.occurred_at ? (
            <span className="event__delay">
              reported <Timestamp value={event.recorded_at} relative />
            </span>
          ) : null}
        </span>
      ),
    },
    {
      id: 'decision',
      header: 'Outcome',
      render: (event) => (
        <Badge
          tone={
            event.decision === 'GRANTED'
              ? 'positive'
              : event.decision === 'DENIED'
                ? 'danger'
                : event.decision === 'ERROR'
                  ? 'warning'
                  : 'neutral'
          }
        >
          {humaniseCode(event.decision)}
        </Badge>
      ),
    },
    {
      id: 'who',
      header: 'Who',
      render: (event) =>
        event.person_name ? (
          /*
            `table__link`, which is not decoration: it carries the 24px minimum
            height that WCAG 2.2 (2.5.8, AA) requires of a target. These links
            were bare, so at 390px they measured 243x20 on every row -- roughly
            forty failures a page, on the two columns somebody on a phone is most
            likely to tap.
          */
          <Link
            to={`/people/${encodeURIComponent(event.subject_external_id ?? '')}`}
            className="table__link"
          >
            {event.person_name}
          </Link>
        ) : event.subject_external_id ? (
          // Matched nobody. The identifier the terminal read is kept so the
          // attempt is traceable, and it is marked rather than left looking
          // like a person whose name failed to load.
          <span className="event__unknown">
            <code className="mono">{event.subject_external_id}</code>
            <Badge tone="warning">Not recognised</Badge>
          </span>
        ) : (
          <span className="muted">—</span>
        ),
    },
    {
      id: 'where',
      header: 'Where',
      render: (event) => (
        <span className="event__where">
          {event.device_serial ? (
            <Link
              to={`/terminals/${encodeURIComponent(event.device_serial)}`}
              className="table__link"
            >
              {event.device_name || event.device_serial}
            </Link>
          ) : (
            <span className="muted">—</span>
          )}
          {/*
            NOT `audit__role`, which uppercases and letterspaces its content.
            That treatment suits an enum -- a role, a target type -- and this is
            the customer's own name for a place. "Lagos Distribution Centre"
            rendered as "LAGOS DISTRIBUTION CENTRE" reads as a code rather than
            as somewhere a person goes.
          */}
          {event.site_name ? <span className="event__site">{event.site_name}</span> : null}
        </span>
      ),
    },
    {
      id: 'why',
      header: 'Why',
      /*
        THE MEANING IS ON THE PAGE, NOT IN A `title`.

        This used to be `<span title={meaning}>{label}</span>`, which puts the
        explanation somewhere three kinds of reader cannot get at it: there is no
        hover on a touch screen, no way to reach it from the keyboard, and screen
        readers disagree about whether to announce it at all. On a phone -- where
        somebody is most likely to be standing next to the person who was refused
        -- the explanation simply did not exist.

        The label stays the scannable thing and the meaning sits under it, quiet
        and readable. The remedy is deliberately NOT repeated per row: it is the
        same sentence for every row sharing a reason, and the summary above the
        table already states it once per reason.

        `describeEventCause` rather than `describeReason` so that an event
        carrying no reason -- the platform's own ERROR events -- still says what
        happened instead of showing an em dash.
      */
      render: (event) => {
        const cause = describeEventCause(event)
        if (!cause) return <span className="muted">—</span>

        /*
          THE REMEDY IS SHOWN HERE ONLY WHERE NOTHING ELSE SHOWS IT.

          A denial's remedy is the same sentence for every row sharing a reason,
          and the summary above the table already states it once per reason —
          repeating it on forty rows would bury the rows in advice.

          An event with NO reason has no such summary: the summary groups
          refusals, and the platform's own errors refused nobody, so they are not
          in it. `ROSTER_CAPACITY_EXCEEDED` would otherwise carry a remedy that
          exists in the vocabulary and appears on no screen. Its row is the only
          place it can go.
        */
        const remedyBelongsHere = !event.reason && Boolean(cause.remedy)

        return (
          <span className="event__why">
            <span className="event__why-label">{cause.label}</span>
            <span className="event__why-meaning">{cause.meaning}</span>
            {remedyBelongsHere ? (
              <span className="event__why-meaning">
                <strong>What to do:</strong> {cause.remedy}
              </span>
            ) : null}
          </span>
        )
      },
    },
    {
      id: 'what',
      header: 'What',
      secondary: true,
      render: (event) => (
        <span className="event__what">
          {humaniseCode(event.event_type)}
          {/*
            THE FEATURE'S NAME, NOT A GENERAL HUMANISATION OF ITS CODE. Both
            produce "Access Control", which is why this survived a first pass —
            but `humaniseCode` turns CHECK_IN into "Check In" while the rest of
            the console, reading the registry, calls it "Check-in". The registry
            is the one place feature names are decided.
          */}
          {event.application ? (
            <span className="audit__role">
              {describeApplication(event.application).label}
            </span>
          ) : null}
        </span>
      ),
    },
  ]

  const page = events.data
  const denials = (page?.events ?? []).filter((event) => event.decision === 'DENIED')

  return (
    <div className="page">
      <PageHeader
        title="Events"
        lead="What happened at your terminals — who was admitted, who was refused, and why."
        actions={<RefreshingIndicator active={events.isFetching && !events.isPending} />}
      />

      {/*
        The counterpart to the note on Activity. Somebody arriving here to find
        out who changed a setting needs sending the other way, once.
      */}
      <InfoNote title="This is the event log, not the operator trail">
        Every record here is something that happened in the field. Changes
        operators made in this console — a terminal disabled, a person removed, a
        role changed — are in <Link to="/activity">Activity</Link>.
      </InfoNote>

      <section className="panel" aria-labelledby="events-filters-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="events-filters-heading">
            Filters
          </h2>
          <p className="field__hint">
            Applied by the server across the whole trail, not to the page below.
            You see events from the sites your account is scoped to.
          </p>
        </div>

        <div className="filter-grid">
          <SelectField
            label="Outcome"
            value={decision}
            placeholder="Any outcome"
            onChange={change((value: string) => setDecision(value as EventDecision | ''))}
            options={[
              { value: 'DENIED', label: 'Denied', description: 'Somebody was refused.' },
              { value: 'GRANTED', label: 'Granted', description: 'Somebody was let in.' },
              { value: 'RECORDED', label: 'Recorded', description: 'Noted without a decision.' },
              { value: 'ERROR', label: 'Error', description: 'The terminal could not decide.' },
            ]}
          />

          <SelectField
            label="Kind of event"
            value={eventType}
            placeholder="Anything"
            onChange={change(setEventType)}
            options={[
              { value: 'ACCESS_GRANTED', label: 'Access granted' },
              { value: 'ACCESS_DENIED', label: 'Access denied' },
              { value: 'PRESENCE', label: 'Presence' },
              { value: 'CREDENTIAL_ENROLLED', label: 'Credential enrolled' },
              { value: 'CREDENTIAL_ENROLMENT_FAILED', label: 'Enrolment failed' },
              { value: 'TAMPER', label: 'Tamper' },
              { value: 'TERMINAL_ONLINE', label: 'Terminal online' },
              { value: 'TERMINAL_OFFLINE', label: 'Terminal offline' },
              /*
                The platform's own error condition, and the only event type it
                emits that carries `decision: ERROR`. The Outcome filter has
                offered "Error" all along while this list omitted the one thing
                that produces one, so an operator could filter to it and had no
                way to ask for it by name.
              */
              { value: 'ROSTER_CAPACITY_EXCEEDED', label: 'Too many people for a terminal' },
            ]}
          />

          <SelectField
            label="Site"
            value={siteId}
            placeholder="Every site you can see"
            onChange={change(setSiteId)}
            options={(sites.data?.sites ?? []).map((site) => ({
              value: site.id,
              label: site.name,
            }))}
          />

          <SearchInput
            label="Terminal serial"
            value={serial}
            onChange={change(setSerial)}
            placeholder="AT-0001"
          />

          <SearchInput
            label="Person"
            value={search}
            onChange={change(setSearch)}
            placeholder={`Name or ${PERSON_ID_LABEL}`}
            busy={events.isFetching && search.trim().length > 0}
          />

          <TextField
            label="From"
            type="date"
            value={from}
            onChange={change(setFrom)}
            hint="Includes the whole day."
          />

          <TextField
            label="To"
            type="date"
            value={to}
            onChange={change(setTo)}
            hint="Includes the whole day."
          />
        </div>

        {filtered ? (
          <button type="button" className="button button--quiet" onClick={clearFilters}>
            Clear filters
          </button>
        ) : null}
      </section>

      {/*
        The denials on this page, explained once at the top rather than
        row-by-row. An operator scanning a log wants to know what KIND of refusal
        is happening before they read forty rows of it — six people refused for
        "no rule covers this" is a configuration mistake, six refused for
        "outside the schedule" is a rota mistake, and the remedies are different.
      */}
      {denials.length > 0 ? <DenialSummary events={denials} /> : null}

      <DataTable
        caption="Events"
        columns={columns}
        rows={page?.events}
        rowKey={(event) => event.id}
        isLoading={events.isPending}
        isFetching={events.isFetching}
        error={events.error}
        onRetry={() => void events.refetch()}
        emptyTitle={filtered ? 'Nothing matched' : 'Nothing recorded yet'}
        emptyDescription={
          filtered
            ? 'No events match these filters. Widen the dates, or clear them.'
            : 'No terminal has reported anything yet. Events appear here as terminals report them — a terminal that is offline uploads what it buffered when it reconnects.'
        }
      />

      {page ? (
        <Pagination
          count={page.count}
          total={page.total}
          offset={page.offset}
          limit={page.limit}
          hasMore={page.has_more}
          onOffsetChange={setOffset}
          noun="events"
          busy={events.isFetching}
        />
      ) : null}
    </div>
  )
}

/**
 * What the refusals on this page have in common.
 *
 * Grouped by REASON rather than by person, because the reason is what an
 * operator acts on. Each carries the remedy from the shared vocabulary, so the
 * same sentence appears here and on the terminal's access preview.
 */
function DenialSummary({ events }: { events: FieldEvent[] }) {
  const byReason = new Map<string, number>()
  for (const event of events) {
    const reason = event.reason ?? 'UNKNOWN'
    byReason.set(reason, (byReason.get(reason) ?? 0) + 1)
  }

  return (
    <section className="panel" aria-labelledby="denials-heading">
      <div className="panel__header">
        <h2 className="panel__title" id="denials-heading">
          Why people were refused on this page
        </h2>
      </div>

      <dl className="detail-list">
        {[...byReason.entries()]
          .sort((a, b) => b[1] - a[1])
          .map(([reason, count]) => {
            const definition = describeReason(reason)
            return (
              <div className="detail-list__row" key={reason}>
                <dt>
                  {definition.label} <Badge tone="danger">{count}</Badge>
                </dt>
                <dd>
                  <p className="rule__detail">{definition.meaning}</p>
                  {definition.remedy ? (
                    <p className="rule__detail">
                      <strong>What to do:</strong> {definition.remedy}
                    </p>
                  ) : null}
                </dd>
              </div>
            )
          })}
      </dl>
    </section>
  )
}
