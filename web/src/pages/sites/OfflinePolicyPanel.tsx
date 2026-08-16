import { useState } from 'react'

import { ApiError } from '../../api/client'
import { MAX_OFFLINE_GRACE_MINUTES, type OfflinePolicy, type Site } from '../../api/types'
import { can } from '../../auth/permissions'
import { Badge } from '../../components/Badge'
import { FormActions, FormError, RadioGroup, TextField } from '../../components/Form'
import { useNotifications } from '../../components/Notifications'
import { InfoNote } from '../../components/states'
import { useUpdateSite } from '../../data/console'
import { useSession } from '../../session/useSession'
import {
  OFFLINE_POLICY_DEFINITIONS,
  describeGrace,
  graceError,
  offlinePolicyDefinition,
  usesGracePeriod,
} from './offlinePolicy'

/**
 * What this site's terminals do when they cannot reach the platform.
 *
 * WHY THIS IS A PANEL OF ITS OWN AND NOT A SETTING. It used to be a number in
 * the free-form settings object, beside the relay hold time — and that number
 * did nothing. The platform reads `sites.offline_policy` and
 * `sites.offline_grace_minutes`, layers them OVER the free-form blob when it
 * builds what a terminal is sent, and now REFUSES a write that puts either name
 * into the blob at all. An operator who set a grace period there had made a
 * safety decision that reached no hardware and been shown no sign of it.
 *
 * So it is here, it is a closed set of alternatives rather than free text, and
 * every option states what it exposes rather than what it is called. Somebody
 * choosing between these is deciding what their building does during a network
 * fault, which is not the same kind of act as changing how long a relay is held.
 *
 * WHAT IS ON SCREEN IS WHAT THE DOOR WILL DO. `offline_policy` and
 * `offline_grace_minutes` are on every site projection, and they are the
 * validated columns a terminal is actually sent — not a copy that could drift.
 * That is worth saying because the alternative was live until very recently: no
 * read returned them, and this panel had to select nothing and admit it could
 * not see the value in force. A safety control the console guesses at reads
 * exactly like one it knows.
 *
 * THE FORM IS SEEDED FROM THE SITE AND THEN LEFT ALONE. It is not re-synced on
 * every render, because an operator part-way through changing a policy should
 * not have their selection replaced by a background refetch. The site's own
 * values are shown separately, above, as the thing in force.
 */
export function OfflinePolicyPanel({ site }: { site: Site }) {
  const { session } = useSession()
  // ADMIN, and it is worth being precise about why, because the neighbouring
  // panel is MANAGER and the two look like the same kind of change.
  //
  // This is carried on `PUT /console/sites/{id}`, which sits in the server's
  // ADMIN group alongside site creation, retirement and key rotation — not on
  // `PUT /console/sites/{id}/settings`, which is MANAGER. Gating it at MANAGER
  // here would offer a control that could only ever produce a 403.
  const mayEdit = can(session, 'manageSites')

  const save = useUpdateSite(site.id)
  const notifications = useNotifications()

  const inForce = offlinePolicyDefinition(site.offline_policy)

  const [policy, setPolicy] = useState<OfflinePolicy>(site.offline_policy)
  const [grace, setGrace] = useState(String(site.offline_grace_minutes))
  const [issue, setIssue] = useState<string | null>(null)

  const needsGrace = usesGracePeriod(policy)
  const chosen = offlinePolicyDefinition(policy)

  // Whether the form differs from what the platform holds. Used to keep the
  // button honest: "Apply" on an unchanged form would push a settings job to
  // every terminal at the site for no reason.
  const changed =
    policy !== site.offline_policy ||
    (needsGrace && Number(grace.trim()) !== site.offline_grace_minutes)

  async function apply() {
    const graceProblem = needsGrace ? graceError(grace) : undefined
    setIssue(graceProblem ?? null)
    if (graceProblem) return

    const minutes = needsGrace ? Number(grace.trim()) : undefined

    try {
      await save.mutateAsync({
        offline_policy: policy,
        // Sent ONLY when the policy uses it. Writing a grace period alongside
        // DENY_ALL would store a number the platform ignores and leave the next
        // operator reading it as though it applied.
        ...(minutes === undefined ? {} : { offline_grace_minutes: minutes }),
      })
      notifications.success(
        `${site.name} will ${policyVerb(policy, minutes)}. Every terminal at this site picks the change up on its next sync.`,
      )
    } catch (error) {
      notifications.failure('Could not change the offline policy.', error)
    }
  }

  return (
    <section className="panel" aria-labelledby="offline-policy-heading">
      <div className="panel__header">
        <h2 className="panel__title" id="offline-policy-heading">
          Behaviour during an outage
        </h2>
        <p className="field__hint">
          What every terminal at this site does when it cannot reach the platform.
          This is a safety decision about the building, and there is no answer the
          platform can pick for you.
        </p>
      </div>

      {/*
        WHAT IS ACTUALLY IN FORCE, stated before the controls and separately from
        them. A selected radio says what the form would send; this says what the
        doors are doing, and the two are different things the moment somebody
        touches the form.
      */}
      <dl className="detail-list">
        <div className="detail-list__row">
          <dt>In force now</dt>
          <dd>
            <Badge tone={site.offline_policy === 'DENY_ALL' ? 'info' : 'warning'}>
              {inForce.label}
            </Badge>
          </dd>
        </div>
        {usesGracePeriod(site.offline_policy) ? (
          <div className="detail-list__row">
            <dt>Grace period</dt>
            <dd>{describeGrace(site.offline_grace_minutes)}</dd>
          </div>
        ) : null}
        <div className="detail-list__row">
          <dt>What that means</dt>
          <dd>{inForce.summary}</dd>
        </div>
      </dl>

      {!mayEdit ? (
        <InfoNote title="Read only">
          Changing what terminals do during an outage is an administrator or owner
          action — it travels with the site itself rather than with its device
          settings. Ask somebody in your company who holds one of those roles.
        </InfoNote>
      ) : null}

      <div className="form">
        <RadioGroup
          legend="When a terminal cannot reach the platform, it should"
          name="offline-policy"
          value={policy}
          onChange={(value) => {
            setPolicy(value as OfflinePolicy)
            setIssue(null)
          }}
          disabled={!mayEdit || save.isPending}
          options={OFFLINE_POLICY_DEFINITIONS.map((definition) => ({
            value: definition.value,
            label: definition.label,
            description: definition.consequence,
          }))}
        />

        {/*
          SHOWN ONLY WHERE IT MEANS SOMETHING. Rendering a grace period beside
          DENY_ALL or CACHED_INDEFINITE — even disabled — reads as a value that
          applies, and the platform ignores it for both.
        */}
        {needsGrace ? (
          <TextField
            label="Grace period"
            type="number"
            required
            value={grace}
            error={issue ?? undefined}
            disabled={!mayEdit || save.isPending}
            onChange={(value) => {
              setGrace(value)
              setIssue(null)
            }}
            onBlur={() => setIssue(graceError(grace) ?? null)}
            hint={
              <>
                How long a terminal keeps deciding from the records it already holds
                before it starts refusing everybody. Between 0 and{' '}
                {MAX_OFFLINE_GRACE_MINUTES.toLocaleString()} minutes — 30 days, which is
                the platform&apos;s own limit and the same one the firmware enforces.
                {graceError(grace) ? null : <> That is {describeGrace(Number(grace.trim()))}.</>}
              </>
            }
          />
        ) : null}

        {mayEdit ? (
          <>
            {changed ? (
              <InfoNote tone="warning" title="This changes what the doors do">
                <p>
                  Applying this replaces{' '}
                  <strong>{inForce.label.toLowerCase()}</strong> with{' '}
                  <strong>{chosen.label.toLowerCase()}</strong> for{' '}
                  {site.terminal_count === 0
                    ? 'every terminal at this site'
                    : `all ${site.terminal_count} terminal${site.terminal_count === 1 ? '' : 's'} at this site`}
                  .
                </p>
                <p>
                  Each one takes effect when that terminal next syncs. Until then it
                  keeps behaving as it was last told to, which is the case worth
                  planning for during an outage — a terminal that is already offline
                  will not hear about this at all until it comes back.
                </p>
              </InfoNote>
            ) : (
              <p className="field__hint">
                This is what the site is already set to. Choose something different to
                change it.
              </p>
            )}

            <FormError
              message={
                save.error
                  ? save.error instanceof ApiError
                    ? save.error.message
                    : 'The offline policy could not be changed.'
                  : null
              }
              requestId={save.error instanceof ApiError ? save.error.requestId : null}
            />

            <FormActions>
              <button
                type="button"
                className="button button--primary"
                onClick={() => void apply()}
                disabled={save.isPending || !changed}
              >
                {save.isPending ? 'Applying…' : 'Apply to every terminal here'}
              </button>
            </FormActions>
          </>
        ) : null}
      </div>
    </section>
  )
}

/** The success message's verb phrase, so the notification says what was chosen. */
function policyVerb(policy: OfflinePolicy, minutes: number | undefined): string {
  switch (policy) {
    case 'DENY_ALL':
      return 'refuse everybody while its terminals are offline'
    case 'CACHED_GRACE':
      return `keep working offline for ${describeGrace(minutes ?? 0)}, then refuse everybody`
    case 'CACHED_INDEFINITE':
      return 'keep working offline for as long as an outage lasts'
  }
}
