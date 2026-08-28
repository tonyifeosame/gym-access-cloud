import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'

import { ApiError } from '../../api/client'
import { can } from '../../auth/permissions'
import { ActiveBadge } from '../../components/Badge'
import { ErrorState, InfoNote, LoadingState, PageHeader } from '../../components/states'
import { useSite } from '../../data/console'
import { useSession } from '../../session/useSession'
import { ProvisionTerminalDialog } from './ClaimCodeDialog'
import { OfflinePolicyPanel } from './OfflinePolicyPanel'
import { SiteFormDialog } from './SiteFormDialog'
import {
  RetireSiteDialog,
  RotateSiteKeyDialog,
  SiteActivationDialog,
} from './SiteLifecycleDialogs'
import { SiteSettingsPanel } from './SiteSettingsPanel'

/**
 * One site: what it is, what state it is in, and what can be done to it.
 *
 * THE HEADER CARRIES ONE ACTION AND THE REST SIT AT THE BOTTOM. It used to hold
 * four buttons of equal weight — Edit, Rotate key, Deactivate, Retire — with a
 * comment claiming the destructive one was "visually apart". It was not: only
 * its fill colour differed, and `.page__actions` wraps, so on a phone Retire
 * could land on a new row directly beside Edit. Four equal buttons at the top of
 * a page is how somebody decommissions a live location while meaning to rename
 * it.
 *
 * So Edit — the frequent, harmless one — stays in the header, and the three that
 * stop hardware working moved into a bordered block at the end of the page,
 * ordered by consequence with the irreversible one last. Reaching them means
 * scrolling past everything the page is actually for, which is the correct
 * amount of friction for actions taken a handful of times in a site's life.
 *
 * PROVISIONING IS NOT ONE OF THEM, and that is the change worth recording rather
 * than a removal. "Register a terminal here" is the common, harmless act and it
 * now happens on the Terminals page, from a code the unit displays on its own
 * screen — no serial number, no cable, and nothing for a customer to mistake for
 * the site key. What is left on this page is the pre-authorised claim code, in
 * the Advanced disclosure, for the case it was built for: authorising a serial
 * before the hardware arrives.
 */
export function SiteDetailPage() {
  const { siteId } = useParams<{ siteId: string }>()
  const navigate = useNavigate()
  const { session } = useSession()
  const query = useSite(siteId)

  const [editing, setEditing] = useState(false)
  const [provisioning, setProvisioning] = useState(false)
  const [rotating, setRotating] = useState(false)
  const [changingActivation, setChangingActivation] = useState(false)
  const [retiring, setRetiring] = useState(false)

  const mayManage = can(session, 'manageSites')

  if (query.isPending) return <LoadingState label="Loading site…" />

  if (query.isError) {
    const error = query.error
    // 404 covers both "no such site" and "another company's site" — the API
    // does not distinguish, deliberately, so neither does this.
    if (error instanceof ApiError && error.isNotFound) {
      return (
        <div className="page">
          <PageHeader
            title="Site not found"
            breadcrumb={<Link to="/sites">Sites</Link>}
          />
          <InfoNote title="Nothing here">
            This site does not exist, or it is not part of your company. It may also
            have been retired.
          </InfoNote>
        </div>
      )
    }
    if (error instanceof ApiError && error.isForbidden) {
      return (
        <div className="page">
          <PageHeader title="Site" breadcrumb={<Link to="/sites">Sites</Link>} />
          <InfoNote title="Not one of your sites">
            This site belongs to your company, but your account is not scoped to it.
            An administrator can grant you access.
          </InfoNote>
        </div>
      )
    }
    return (
      <div className="page">
        <PageHeader title="Site" breadcrumb={<Link to="/sites">Sites</Link>} />
        <ErrorState error={error} onRetry={() => void query.refetch()} />
      </div>
    )
  }

  const site = query.data

  return (
    <div className="page">
      <PageHeader
        title={site.name}
        breadcrumb={<Link to="/sites">Sites</Link>}
        lead={site.address || undefined}
        actions={
          mayManage ? (
            <button type="button" className="button" onClick={() => setEditing(true)}>
              Edit
            </button>
          ) : null
        }
      />

      {!site.active ? (
        <InfoNote tone="warning" title="This site is deactivated">
          Its provisioning key and all {site.terminal_count} terminal
          {site.terminal_count === 1 ? '' : 's'} are refused while it stays this way.
          Nothing has been deleted — reactivating restores service immediately.
        </InfoNote>
      ) : null}

      {/*
        THREE CARDS, DOWN FROM FIVE, and each removal has a reason of its own
        rather than a general wish for less.

        PROVISIONING KEY went because it could never be filled.
        `models.ConsoleSite` carries no prefix and `consoleSiteColumns` selects
        none, so it always read "Not reported" — under thirty-nine words
        explaining, to a customer, that our own read endpoints do not return a
        field. A key is real only in the one-time panel that mints it.

        ADDED went for the reason the people list dropped its "Updated" column: a
        timestamp nobody sorts by, filters on or acts on, costing a full card
        here and a full line on a phone.

        STATUS STAYS, and it is worth saying why, because it looks like a
        duplicate of the banner above it. The banner is negative-only — it
        appears when a site is deactivated and says what that costs. Removing
        this card would leave an active site with no positive statement of its
        state anywhere on the page, so somebody who arrived here by link would
        have to infer it from the absence of a warning.
      */}
      <section className="cards">
        <article className="card">
          <h2 className="card__title">Status</h2>
          <p className="card__value">
            <ActiveBadge active={site.active} />
          </p>
        </article>

        <article className="card">
          <h2 className="card__title">Terminals</h2>
          <p className="card__value">{site.terminal_count}</p>
          <p className="card__detail">installed at this site</p>
        </article>

        <article className="card">
          <h2 className="card__title">Time zone</h2>
          <p className="card__value">
            <code className="mono">{site.timezone}</code>
          </p>
          <p className="card__detail">where the hardware stands</p>
        </article>
      </section>

      {/*
        ORDER MATTERS HERE. The outage policy is a safety decision about the
        building and sits above the device configuration, which is a list of
        preferences. It is also where the settings panel below points for the
        grace period it no longer edits, so it has to be the thing you find first.
      */}
      <OfflinePolicyPanel site={site} />

      <SiteSettingsPanel siteId={site.id} siteName={site.name} />

      {mayManage ? (
        <AdvancedProvisioningPanel onIssueClaimCode={() => setProvisioning(true)} />
      ) : null}

      {mayManage ? (
        <DangerZone
          site={site}
          onRotate={() => setRotating(true)}
          onChangeActivation={() => setChangingActivation(true)}
          onRetire={() => setRetiring(true)}
        />
      ) : null}

      {editing ? (
        <SiteFormDialog open site={site} onClose={() => setEditing(false)} />
      ) : null}
      {provisioning ? (
        <ProvisionTerminalDialog open site={site} onClose={() => setProvisioning(false)} />
      ) : null}
      {rotating ? (
        <RotateSiteKeyDialog open site={site} onClose={() => setRotating(false)} />
      ) : null}
      {changingActivation ? (
        <SiteActivationDialog
          open
          site={site}
          onClose={() => setChangingActivation(false)}
        />
      ) : null}
      {retiring ? (
        <RetireSiteDialog
          open
          site={site}
          onClose={() => setRetiring(false)}
          // The site this page is about no longer exists; staying here would
          // show a 404 where a detail view used to be.
          onRetired={() => navigate('/sites', { replace: true })}
        />
      ) : null}
    </div>
  )
}

/**
 * The installer's path, kept and demoted.
 *
 * A CLAIM CODE IS STILL THE RIGHT TOOL for one job: pre-authorising a serial
 * that has not arrived yet, so an integrator can commission a unit the moment it
 * is unboxed without an administrator being reachable. It is the wrong tool for
 * a customer with a box, because it is minted FOR a serial and the serial is
 * readable only over a USB cable — which is the whole reason the announce flow
 * exists.
 *
 * SO IT IS BEHIND A DISCLOSURE RATHER THAN DELETED. Somebody who needs it knows
 * they need it; a customer setting up their first door must not find it first
 * and conclude that provisioning requires a laptop and a serial cable.
 *
 * ONE PARAGRAPH, NOT THREE. The dialog this opens says what a claim code is,
 * what it costs and where the ordinary path is, at length and at the moment
 * somebody is deciding to use one. Saying all of it again out here meant a
 * customer read the same warning twice and an installer scrolled past it to
 * reach the button.
 *
 * WHAT IS DELIBERATELY NOT OFFERED HERE, and never should be: the site
 * provisioning key as an alternative. It registers every terminal at this site
 * for ever, it cannot be recovered, and rotating it locks out every unit
 * depending on it.
 */
function AdvancedProvisioningPanel({ onIssueClaimCode }: { onIssueClaimCode: () => void }) {
  return (
    <section className="panel" aria-labelledby="advanced-provisioning-title">
      <details className="disclosure">
        <summary className="disclosure__summary" id="advanced-provisioning-title">
          Advanced: pre-authorise a terminal for an installer
        </summary>

        <div className="disclosure__body">
          <p className="field__hint">
            Authorises one serial number before the hardware arrives, so whoever
            installs it can bring it up without waiting for an administrator. Most
            terminals do not need this — they are added from{' '}
            <Link to="/terminals">the Terminals page</Link>, using the code the unit
            shows on its own screen.
          </p>

          <button type="button" className="button" onClick={onIssueClaimCode}>
            Issue a claim code
          </button>
        </div>
      </details>
    </section>
  )
}

/**
 * The three actions that stop hardware working.
 *
 * GROUPED, LABELLED AND LAST, rather than sitting in the page header looking
 * like Edit. Each is still shaped differently in its own dialog — that is where
 * deactivation and retirement are kept from being mistaken for one another — but
 * a customer should not have to read four button labels at the top of the page
 * to find out which one is safe.
 *
 * ORDERED BY CONSEQUENCE: rotate a credential, suspend the site, remove it. The
 * irreversible one is last and is the only one carrying the danger fill. On a
 * phone the block stacks, so nothing wraps into a position that puts Retire
 * where Deactivate was a moment ago.
 */
function DangerZone({
  site,
  onRotate,
  onChangeActivation,
  onRetire,
}: {
  site: { active: boolean }
  onRotate: () => void
  onChangeActivation: () => void
  onRetire: () => void
}) {
  return (
    <section className="panel panel--danger" aria-labelledby="site-danger-heading">
      <div className="panel__header">
        <h2 className="panel__title" id="site-danger-heading">
          Site administration
        </h2>
        <p className="field__hint">
          These change or stop what the hardware at this site can do. Each one
          explains what it affects before it happens.
        </p>
      </div>

      <div className="danger-actions">
        {/*
          NAMES THE KEY. "Rotate key" sat beside "Deactivate" and "Retire" on a
          page that also talks about device credentials and claim codes, so the
          one word "key" did not say which of three credentials was about to
          stop working. The dialog behind it, the panel that follows and the
          audit trail all say "provisioning key"; this is the only place that
          did not, and it is the one an operator presses.
        */}
        <button type="button" className="button" onClick={onRotate}>
          Rotate provisioning key
        </button>
        <button type="button" className="button" onClick={onChangeActivation}>
          {site.active ? 'Deactivate' : 'Reactivate'}
        </button>
        <button type="button" className="button button--danger" onClick={onRetire}>
          Retire
        </button>
      </div>
    </section>
  )
}
