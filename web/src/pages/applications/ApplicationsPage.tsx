import { useState } from 'react'
import { Link } from 'react-router-dom'

import { MULTI_PURPOSE, type ApplicationCode } from '../../api/types'
import { describeApplication } from '../../applications/registry'
import { can } from '../../auth/permissions'
import { Badge } from '../../components/Badge'
import { useNotifications } from '../../components/Notifications'
import { ErrorState, InfoNote, LoadingState, PageHeader } from '../../components/states'
import { useApplications, useUpdateApplication } from '../../data/console'
import { useSession } from '../../session/useSession'
import { TurnOffFeatureDialog } from './TurnOffFeatureDialog'

/**
 * What this company uses AccessLink for.
 *
 * ---------------------------------------------------------------------------
 * WHAT THIS SCREEN STOPPED SAYING, AND WHY
 * ---------------------------------------------------------------------------
 *
 * It used to teach the customer a four-state internal model — available,
 * enabled, configured, operational — and then mark every capability with two
 * badges, one of which read "Not built yet" or "Partly built". Under each row
 * it printed a paragraph naming precisely what was unfinished; the Registration
 * one described how enrolled biometric material is not distributed between
 * terminals. Beneath that sat the raw platform code, `ACCESS_CONTROL`.
 *
 * All of that was accurate and none of it belonged here. It is a development
 * status report rendered into a settings screen, and its effect on a
 * non-technical customer is to make a working product look unfinished.
 *
 * WHERE THE HONESTY LIVES NOW: docs/market-readiness.md, at the repository
 * root. That is the document product and sales own, and the right home for
 * "what we have and have not finished" — a commitment made to a buyer in the
 * place a buyer is given it, rather than a paragraph a customer trips over
 * while switching a feature on.
 *
 * ONE THING THE OLD COPY DID THAT THIS SCREEN NO LONGER DOES, RECORDED HERE
 * DELIBERATELY. The badges also warned an owner that switching a capability on
 * would not make it happen — Attendance can be enabled and no attendance is
 * recorded. This page no longer distinguishes those, because the module it read
 * that from no longer reports it. The risk is a real one and it is flagged for
 * a product decision rather than solved by re-inventing build status here.
 *
 * NOTHING WAS REMOVED. Every capability the server offers is still listed, and
 * every one can still be switched on and off by an owner.
 *
 * ---------------------------------------------------------------------------
 * FOUR CONCEPTS MEET HERE AND ARE ROUTINELY CONFUSED
 * ---------------------------------------------------------------------------
 *
 *   Platform capability      what AccessLink can do at all. The server's
 *                            `available` list, not a constant in this build.
 *   Company application      which of those THIS company has enabled. Owned
 *                            here, and the only thing this page writes.
 *   Terminal mode            what ONE terminal is pointed at. Owned on the
 *                            terminal, and never set from here.
 *   Operator permission      who may read or change any of the above.
 *
 * MULTI_PURPOSE IS NOT AN APPLICATION. It is a terminal mode meaning "serve
 * whatever this company has enabled", and the server rejects it as a company
 * capability. It is filtered out defensively below as well.
 *
 * THE CATALOG COMES FROM THE SERVER. `available` is the authority, so a
 * capability added to the platform appears here without a frontend release.
 */
export function ApplicationsPage() {
  const { session } = useSession()
  const query = useApplications()
  const update = useUpdateApplication()
  const notifications = useNotifications()

  /*
    THE FEATURE WAITING FOR A TURN-OFF TO BE CONFIRMED.

    This list fired the change the moment the button was pressed, while the
    detail page for the same action asked first and named the consequence. The
    list is where the toggles are, so the unguarded path was the one nearly
    everybody used. Both now go through the same dialog, which is a component
    rather than two copies of the copy.

    Turning ON stays immediate: it takes nothing away, and a question there
    teaches people to dismiss the one that matters.
  */
  const [turningOff, setTurningOff] = useState<ApplicationCode | null>(null)

  // Reading is ADMIN in this console. The API itself allows any operator to
  // read, but what a company is FOR is administrative context rather than
  // day-to-day information, and the write is OWNER either way. UNCHANGED.
  const mayConfigure = can(session, 'configureApplications')

  if (query.isPending) return <LoadingState label="Loading features…" />
  if (query.isError) {
    return (
      <div className="page">
        <PageHeader title="Features" />
        <ErrorState error={query.error} onRetry={() => void query.refetch()} />
      </div>
    )
  }

  const { available, enabled } = query.data

  // Defensive: MULTI_PURPOSE must never appear as something a company enables,
  // whatever the server sends.
  const catalog = available.filter((code) => code !== MULTI_PURPOSE)
  const enabledSet = new Set(enabled.filter((code) => code !== MULTI_PURPOSE))

  async function toggle(code: ApplicationCode, next: boolean) {
    const label = describeApplication(code).label
    try {
      await update.mutateAsync({ code, body: { enabled: next } })
      notifications.success(next ? `${label} turned on` : `${label} turned off`)
    } catch (error) {
      notifications.failure(`Could not turn ${label} ${next ? 'on' : 'off'}.`, error)
    }
  }

  function renderCapability(code: ApplicationCode) {
    return (
      <Capability
        key={code}
        code={code}
        isEnabled={enabledSet.has(code)}
        mayConfigure={mayConfigure}
        busy={update.isPending}
        onTurnOn={() => void toggle(code, true)}
        onTurnOff={() => setTurningOff(code)}
      />
    )
  }

  return (
    <div className="page">
      <PageHeader
        title="Features"
        lead="What your company uses AccessLink for. Turn a feature on to make it available to your terminals."
      />

      {!mayConfigure ? (
        <InfoNote title="Read only">
          You can see which features are on but not change them. Only an owner
          decides what a company uses.
        </InfoNote>
      ) : null}

      {catalog.length === 0 ? (
        <InfoNote title="No features available">
          This platform reports no features for your company.
        </InfoNote>
      ) : (
        <ul className="capability-list">{catalog.map(renderCapability)}</ul>
      )}

      {turningOff ? (
        <TurnOffFeatureDialog
          open
          label={describeApplication(turningOff).label}
          onConfirm={() => toggle(turningOff, false)}
          onClose={() => setTurningOff(null)}
        />
      ) : null}

      <section className="panel" aria-labelledby="applications-model-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="applications-model-heading">
            How this relates to your terminals
          </h2>
        </div>
        <p>
          A feature turned on here becomes available to the whole company. Each
          terminal is then <strong>assigned</strong> to one of them, or left
          multi-purpose to serve all of them — that assignment is made per terminal
          under <Link to="/terminals">Terminals</Link>.
        </p>
        <p className="field__hint">
          Multi-purpose is a terminal setting, not a feature, so it does not appear
          in the list above. Turning a feature off leaves any terminal assigned to
          it configured but resolving to nothing, rather than silently reassigning
          it.
        </p>
      </section>
    </div>
  )
}

/**
 * One feature: what it is, whether it is on, and the control to change that.
 *
 * ONE BADGE, NOT TWO. The second badge used to report whether the platform had
 * built the thing. Which list the row is in now carries that, so the row itself
 * answers only the question the customer asked: is this on?
 */
function Capability({
  code,
  isEnabled,
  mayConfigure,
  busy,
  onTurnOn,
  onTurnOff,
}: {
  code: ApplicationCode
  isEnabled: boolean
  mayConfigure: boolean
  busy: boolean
  /** Immediate: turning something on takes nothing away. */
  onTurnOn: () => void
  /** Opens the confirmation. Never writes directly — see the dialog. */
  onTurnOff: () => void
}) {
  const definition = describeApplication(code)

  return (
    <li className="capability" data-enabled={isEnabled}>
      <div className="capability__main">
        <h2 className="capability__title">
          <Link to={`/settings/applications/${definition.slug}`}>{definition.label}</Link>
          {isEnabled ? <Badge tone="positive">On</Badge> : <Badge>Off</Badge>}
        </h2>
        <p className="capability__description">{definition.description}</p>
      </div>

      {mayConfigure ? (
        <div className="capability__action">
          <button
            type="button"
            className={isEnabled ? 'button' : 'button button--primary'}
            disabled={busy}
            onClick={isEnabled ? onTurnOff : onTurnOn}
          >
            {isEnabled ? 'Turn off' : 'Turn on'}
          </button>
        </div>
      ) : null}
    </li>
  )
}
