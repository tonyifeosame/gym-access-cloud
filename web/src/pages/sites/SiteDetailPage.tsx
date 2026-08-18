import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'

import { ApiError } from '../../api/client'
import { can } from '../../auth/permissions'
import { ActiveBadge } from '../../components/Badge'
import { ErrorState, InfoNote, LoadingState, PageHeader } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
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
 * THE LIFECYCLE ACTIONS ARE ORDERED AND SEPARATED BY CONSEQUENCE — edit, rotate,
 * deactivate, then retire — with the irreversible one last and visually apart. A
 * row of equal-looking buttons is how somebody retires a live location while
 * meaning to rename it.
 *
 * PROVISIONING IS NO LONGER ONE OF THEM, and that is the change worth recording
 * rather than a removal. "Register a terminal here" is the common, harmless act
 * and it now happens on the Terminals page, from a code the unit displays on its
 * own screen — no serial number, no cable, and nothing for a customer to
 * mistake for the site key. What is left on this page is the pre-authorised
 * claim code, in the Advanced panel at the bottom, for the case it was built
 * for: authorising a serial before the hardware arrives.
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
            <>
              {/*
                NO LONGER THE PRIMARY ACTION HERE. Adding a terminal is done from
                Terminals now, with a code the unit displays on its own screen,
                and that path needs no serial number and no cable. The claim code
                stays for the case it was built for -- pre-authorising hardware
                that has not arrived yet -- and has moved into the Advanced panel
                below, where somebody who knows they need it will look and a
                customer setting up their first door will not.
              */}
              <button type="button" className="button" onClick={() => setEditing(true)}>
                Edit
              </button>
              <button type="button" className="button" onClick={() => setRotating(true)}>
                Rotate key
              </button>
              <button
                type="button"
                className="button"
                onClick={() => setChangingActivation(true)}
              >
                {site.active ? 'Deactivate' : 'Reactivate'}
              </button>
              {/* Apart from the rest, and the only one styled as destructive. */}
              <button
                type="button"
                className="button button--danger"
                onClick={() => setRetiring(true)}
              >
                Retire
              </button>
            </>
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

        <article className="card">
          <h2 className="card__title">Provisioning key</h2>
          <p className="card__value">
            {site.api_key_prefix ? (
              <code className="mono">{site.api_key_prefix}…</code>
            ) : (
              <span className="muted">Not reported</span>
            )}
          </p>
          <p className="card__detail">
            {/*
              TWO SEPARATE FACTS, and they used to be run together as "not
              shown", which reads as a deliberate redaction and is only half
              right.

              The KEY is deliberately unreachable: stored as a SHA-256, returned
              only when created or rotated. That is the design.

              The PREFIX is not secret and is meant to be readable — it is the
              12 characters that say which key a site is on without being
              reconstructible. The column exists and the create and rotate
              responses carry it, but `models.ConsoleSite` has no field for it,
              so no GET returns one. This card can therefore only show a prefix
              during the session that minted it. That is a gap in the API rather
              than a decision, and calling it "not shown" implied otherwise.
            */}
            {site.api_key_prefix
              ? 'The non-secret prefix of the key issued in this session. The key itself is shown once and cannot be recovered.'
              : 'The key itself is shown once and cannot be recovered. The non-secret prefix is not returned by any read endpoint, so it can only be displayed just after the key is created or rotated.'}
          </p>
        </article>

        <article className="card">
          <h2 className="card__title">Added</h2>
          <p className="card__value">
            <Timestamp value={site.created_at} />
          </p>
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
 * WHAT IS DELIBERATELY NOT OFFERED HERE, and never should be: the site
 * provisioning key as an alternative. It registers every terminal at this site
 * for ever, it cannot be recovered, and rotating it locks out every unit
 * depending on it.
 */
function AdvancedProvisioningPanel({ onIssueClaimCode }: { onIssueClaimCode: () => void }) {
  return (
    <section className="panel" aria-labelledby="advanced-provisioning-title">
      <details>
        <summary id="advanced-provisioning-title">
          Advanced: pre-authorise a terminal for an installer
        </summary>

        <p className="card__detail">
          Most terminals are added from{' '}
          <Link to="/terminals">the Terminals page</Link>, using the code the unit
          shows on its own screen. No serial number and no cable are needed, and it is
          the path to use unless the hardware is not in front of you.
        </p>

        <p className="card__detail">
          A <strong>claim code</strong> is for the other case: authorising a specific
          serial before the hardware arrives, so whoever installs it can bring it up
          without waiting for an administrator. It works once, for that one serial, and
          expires. You need the serial number, and the installer needs a serial cable to
          enter it at the unit.
        </p>

        <button type="button" className="button" onClick={onIssueClaimCode}>
          Issue a claim code
        </button>
      </details>
    </section>
  )
}
