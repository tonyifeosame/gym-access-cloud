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
 * WHAT IS ON SCREEN IS WHAT THE DOOR WILL DO. `offline_policy` and
 * `offline_grace_minutes` are on every site projection, and they are the
 * validated columns a terminal is actually sent — not a copy that could drift.
 *
 * THE PANEL RESTS ON THE ANSWER, NOT THE QUESTION, and that is the change worth
 * recording. It used to render the whole editor unconditionally: three options,
 * each carrying a paragraph of consequence, roughly two hundred and forty words
 * permanently open on a page somebody had come to for a terminal count. Worse,
 * because the radio was pre-selected to the value in force, the current policy
 * was stated three times over — as a badge, as a summary, and again as the
 * selected option's consequence.
 *
 * So the resting state is one badge and one sentence, and the alternatives
 * appear when somebody says they want to change something. THE CONSEQUENCES ARE
 * NOT WHAT WAS CUT: every word of them is still beside its option, at the moment
 * it is being weighed, which is the only moment it was ever for.
 *
 * NON-ADMINS NEVER SEE THE EDITOR AT ALL. It is carried on
 * `PUT /console/sites/{id}`, which sits in the server's ADMIN group alongside
 * site creation, retirement and key rotation — not on the MANAGER settings
 * route. A manager or viewer used to get the full radio group rendered and
 * disabled: a hundred and eighty words of choices they could not make, below the
 * one sentence that told them what was true.
 *
 * THE FORM IS SEEDED WHEN IT OPENS AND THEN LEFT ALONE, so an operator part-way
 * through changing a policy does not have their selection replaced by a
 * background refetch. What is in force is shown separately, above.
 */
export function OfflinePolicyPanel({ site }: { site: Site }) {
  const { session } = useSession()
  const mayEdit = can(session, 'manageSites')

  const save = useUpdateSite(site.id)
  const notifications = useNotifications()

  const inForce = offlinePolicyDefinition(site.offline_policy)

  const [editing, setEditing] = useState(false)
  const [policy, setPolicy] = useState<OfflinePolicy>(site.offline_policy)
  const [grace, setGrace] = useState(String(site.offline_grace_minutes))
  const [issue, setIssue] = useState<string | null>(null)

  const needsGrace = usesGracePeriod(policy)
  const chosen = offlinePolicyDefinition(policy)

  // Whether the form differs from what the platform holds. Used to keep the
  // button honest: applying an unchanged form would push a settings job to every
  // terminal at the site for no reason.
  const changed =
    policy !== site.offline_policy ||
    (needsGrace && Number(grace.trim()) !== site.offline_grace_minutes)

  function openEditor() {
    // Re-seed from the site on the way in. The panel may have been opened,
    // abandoned and reopened after a colleague changed the policy, and starting
    // from a stale selection is how somebody reverts a change they never saw.
    setPolicy(site.offline_policy)
    setGrace(String(site.offline_grace_minutes))
    setIssue(null)
    setEditing(true)
  }

  function closeEditor() {
    setIssue(null)
    setEditing(false)
  }

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
      // Back to the resting state, which now shows what was just applied.
      setEditing(false)
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
        </p>
      </div>

      {/*
        WHAT IS ACTUALLY IN FORCE. In the resting state this is the whole panel,
        and it is deliberately the shortest true statement of it: the policy, the
        grace period where one applies, and one sentence of what that means at a
        door. Once the editor is open this stays, because a selected radio says
        what the form WOULD send and these say what the doors are doing — two
        different things the moment somebody touches the form.
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

      {/*
        NOTHING BELOW THIS LINE FOR A MANAGER OR A VIEWER. They have read the
        answer; the alternatives are an administrator's to weigh, and rendering
        them disabled was offering a decision that could only ever produce a 403.
        Who to ask is on the panel instead, in one line.
      */}
      {!mayEdit ? (
        <p className="field__hint">
          Changing this travels with the site itself rather than with its device
          settings, so it is an administrator or owner action.
        </p>
      ) : !editing ? (
        <FormActions>
          <button type="button" className="button" onClick={openEditor}>
            Change policy
          </button>
        </FormActions>
      ) : (
        <div className="form">
          <RadioGroup
            legend="When a terminal cannot reach the platform, it should"
            name="offline-policy"
            value={policy}
            onChange={(value) => {
              setPolicy(value as OfflinePolicy)
              setIssue(null)
            }}
            disabled={save.isPending}
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
              disabled={save.isPending}
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
                  the longest this can be set to.
                  {graceError(grace) ? null : <> That is {describeGrace(Number(grace.trim()))}.</>}
                </>
              }
            />
          ) : null}

          {/*
            THE IMPACT NOTE, and it is a plain block rather than a warning
            notice. It is only rendered once the form differs from what is in
            force, so it never has to say "nothing has changed yet" — the button
            beside it being disabled says that.
          */}
          {changed ? (
            <InfoNote tone="warning" headingLevel={3} title="This changes what your terminals do">
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
                Each one takes effect when that terminal next syncs. A terminal that is
                already offline will not hear about this until it comes back, which is
                the case worth planning for.
              </p>
            </InfoNote>
          ) : null}

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
              className="button button--quiet"
              onClick={closeEditor}
              disabled={save.isPending}
            >
              Cancel
            </button>
            <button
              type="button"
              className="button button--primary"
              onClick={() => void apply()}
              disabled={save.isPending || !changed}
            >
              {save.isPending ? 'Applying…' : 'Apply to every terminal here'}
            </button>
          </FormActions>
        </div>
      )}
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
