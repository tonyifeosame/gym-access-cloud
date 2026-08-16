import { useEffect, useId, useRef, useState } from 'react'

import { ApiError } from '../../api/client'
import {
  DEFAULT_CLAIM_MINUTES,
  MAX_CLAIM_MINUTES,
  MAX_SERIAL_LENGTH,
  type ClaimCodeResponse,
  type Site,
} from '../../api/types'
import { CopyButton } from '../../components/CopyButton'
import { Dialog } from '../../components/Dialog'
import { FormActions, FormError, TextField } from '../../components/Form'
import { useNotifications } from '../../components/Notifications'
import { InfoNote } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { submitErrorMessage, useForm, validators } from '../../components/useForm'
import { useIssueClaimCode } from '../../data/console'

/**
 * Provisioning a terminal without handing out the site key.
 *
 * THE PROBLEM THIS REPLACES. Registering hardware used to mean putting the
 * site's provisioning key on an installer's laptop. That key registers ANY
 * terminal at the site, for ever, cannot be recovered, and rotating it locks out
 * every unit still depending on it. It is the wrong credential to give somebody
 * who needs to bring up one door on one afternoon, and the console must never
 * offer it as the easier alternative — which is why nothing in this file mentions
 * the key as a fallback path.
 *
 * A claim code is the right-sized credential: ONE serial, ONCE, for minutes.
 * Intercepting one is close to worthless because the serial is bound into it
 * server-side and checked at redemption, so a code lifted in transit only works
 * for the exact unit it was minted for.
 *
 * THE FOUR FACTS AN INSTALLER MUST LEAVE WITH, and every one of them is stated
 * on screen rather than in documentation:
 *
 *   1. The code is shown once and cannot be read back. The server stores only
 *      its SHA-256.
 *   2. It works exactly once. Redemption consumes it.
 *   3. It expires, at a time this panel names.
 *   4. Issuing another code for the same serial KILLS THIS ONE. That is the fact
 *      most likely to be discovered at a door: somebody re-issues "to be safe"
 *      and the printout in the installer's pocket has silently stopped working.
 */

// ---------------------------------------------------------------------------
// The one-time panel
// ---------------------------------------------------------------------------

/**
 * A minted claim code, shown once.
 *
 * BORROWS THE SITE-CREDENTIAL PANEL'S DELIBERATELY OBSTRUCTIVE SHAPE — warning
 * first, explicit acknowledgement, no accidental dismissal — for the same
 * reason: the cost of a reflexive close is a credential nobody has, and here it
 * also means an installer standing at a door with nothing to type.
 *
 * WHAT THIS MUST NEVER BE CHANGED TO DO: write the code to localStorage,
 * sessionStorage, a URL, a query key, the React Query cache, or a log. It lives
 * in a prop for the life of this panel, and the caller resets the mutation that
 * produced it when the panel closes.
 */
export function ClaimCodePanel({
  claim,
  onDismiss,
}: {
  claim: ClaimCodeResponse
  onDismiss: () => void
}) {
  const [acknowledged, setAcknowledged] = useState(false)
  const [copied, setCopied] = useState(false)
  const acknowledgeId = useId()
  const warningId = useId()
  const headingRef = useRef<HTMLHeadingElement | null>(null)

  // Focus the heading when the panel appears. Without this a keyboard user is
  // still on the button that submitted the form, several elements away from a
  // credential they have one chance to read.
  useEffect(() => {
    headingRef.current?.focus()
  }, [])

  return (
    <section
      className="credential"
      role="alertdialog"
      aria-modal="false"
      aria-labelledby={`${warningId}-heading`}
      aria-describedby={warningId}
    >
      <h2 className="credential__heading" id={`${warningId}-heading`} tabIndex={-1} ref={headingRef}>
        Claim code for {claim.serial_number}
      </h2>

      <p className="credential__warning" id={warningId}>
        <strong>
          This code is shown once, works once, and cannot be recovered.
        </strong>{' '}
        It registers the single terminal with serial{' '}
        <code className="mono">{claim.serial_number}</code> at {claim.site_name}, and
        nothing else. If it is lost, issue another — which invalidates this one.
      </p>

      <div className="credential__value">
        <label className="field__label" htmlFor={`${warningId}-code`}>
          Claim code
        </label>
        <div className="credential__row">
          {/* readOnly rather than disabled: a disabled input cannot be focused or
              selected, which removes the manual fallback when the clipboard is
              unavailable. */}
          <input
            id={`${warningId}-code`}
            className="field__input field__input--mono credential__input"
            value={claim.claim_code}
            readOnly
            spellCheck={false}
            onFocus={(event) => event.currentTarget.select()}
          />
          <CopyButton
            value={claim.claim_code}
            label="Copy code"
            onCopied={setCopied}
            describedBy={warningId}
          />
        </div>
        <p className="field__hint">
          Expires <Timestamp value={claim.expires_at} relative />. Identified in the
          activity trail as <code className="mono">{claim.code_prefix}…</code>, which
          is all that is recorded — the trail can say which code was issued without
          being a place to read one.
        </p>
      </div>

      {/*
        THE FACT MOST LIKELY TO STRAND SOMEBODY. Reported by the server precisely
        so a console can say it, rather than an installer discovering it at a
        door.
      */}
      {claim.superseded_codes > 0 ? (
        <p className="credential__impact">
          <strong>
            {claim.superseded_codes} earlier code
            {claim.superseded_codes === 1 ? '' : 's'} for this serial stopped working
          </strong>{' '}
          the moment this was issued. If somebody is already holding one, they now need
          this code instead.
        </p>
      ) : null}

      <InfoNote title="What the installer does with it">
        <p>
          At the terminal, they enter this code and its serial number. The platform
          checks the pair, issues that unit its own device credential, and the code is
          consumed. The terminal is then a normal terminal on this site — nothing about
          it is provisional.
        </p>
        <p>
          The code is bound to <code className="mono">{claim.serial_number}</code>: it
          will not register any other unit, so it can safely be read out over a phone
          or a serial console.
        </p>
      </InfoNote>

      <div className="credential__acknowledge">
        <div className="checkbox">
          <input
            id={acknowledgeId}
            type="checkbox"
            className="checkbox__input"
            checked={acknowledged}
            onChange={(event) => setAcknowledged(event.target.checked)}
          />
          <label className="checkbox__label" htmlFor={acknowledgeId}>
            I have copied this code and can get it to whoever is installing the terminal
          </label>
        </div>

        <button
          type="button"
          className="button button--primary"
          onClick={onDismiss}
          disabled={!acknowledged}
        >
          Done
        </button>
      </div>

      {/* A nudge, not a gate: they may have copied it by hand. */}
      {acknowledged || copied ? null : (
        <p className="credential__nudge">You have not copied the code yet.</p>
      )}
    </section>
  )
}

// ---------------------------------------------------------------------------
// The dialog
// ---------------------------------------------------------------------------

interface ClaimValues extends Record<string, unknown> {
  serial_number: string
  expires_in_minutes: string
}

/**
 * Two steps in one dialog: ask for the serial, then read the code once.
 *
 * They cannot be separate dialogs, for the same reason key rotation's cannot be:
 * the code exists only in the response to the first step, so closing between the
 * two would destroy it.
 */
export function ProvisionTerminalDialog({
  open,
  site,
  onClose,
}: {
  open: boolean
  site: Site
  onClose: () => void
}) {
  const issue = useIssueClaimCode(site.id)
  const notifications = useNotifications()
  const [claim, setClaim] = useState<ClaimCodeResponse | null>(null)

  const form = useForm<ClaimValues>({
    initialValues: {
      serial_number: '',
      expires_in_minutes: String(DEFAULT_CLAIM_MINUTES),
    },
    validate: (values) => ({
      serial_number:
        validators.required(values.serial_number, 'A serial number') ??
        // The firmware holds a serial in char[16], so the server refuses anything
        // longer. Caught here so an operator is told which field is wrong rather
        // than being handed a 400.
        validators.maxLength(values.serial_number.trim(), MAX_SERIAL_LENGTH, 'A serial number'),
      expires_in_minutes: expiryError(values.expires_in_minutes),
    }),
    onSubmit: async (values) => {
      const minutes = Number(values.expires_in_minutes.trim())
      const issued = await issue.mutateAsync({
        serial_number: values.serial_number.trim(),
        expires_in_minutes: minutes,
      })
      setClaim(issued)
      notifications.success(
        `Claim code issued for ${issued.serial_number}. It is shown once and works once.`,
      )
    },
  })

  function dismiss() {
    // Clear both copies of the credential: this component's, and the one the
    // mutation retains in `data`.
    setClaim(null)
    issue.reset()
    form.reset({ serial_number: '', expires_in_minutes: String(DEFAULT_CLAIM_MINUTES) })
    onClose()
  }

  if (claim) {
    return (
      <Dialog open={open} title="Claim code issued" size="wide" dismissible={false} onClose={dismiss}>
        <ClaimCodePanel claim={claim} onDismiss={dismiss} />
      </Dialog>
    )
  }

  const error = form.submitError

  return (
    <Dialog
      open={open}
      title={`Provision a terminal at ${site.name}`}
      description="Issues a one-time code for one terminal. The site's provisioning key is not involved and is never handed out for this."
      dismissible={!form.submitting}
      onClose={onClose}
      size="wide"
    >
      <form className="form" onSubmit={(event) => void form.handleSubmit(event)} noValidate>
        <TextField
          label="Serial number"
          required
          mono
          value={form.values.serial_number}
          error={form.errors.serial_number}
          onChange={(value) => form.setValue('serial_number', value)}
          onBlur={() => form.touch('serial_number')}
          disabled={form.submitting}
          hint={`Exactly as it is printed on the unit. The code only ever registers this serial, which is what makes it safe to read out. At most ${MAX_SERIAL_LENGTH} characters — the length the firmware can hold.`}
        />

        <TextField
          label="Valid for"
          type="number"
          required
          value={form.values.expires_in_minutes}
          error={form.errors.expires_in_minutes}
          onChange={(value) => form.setValue('expires_in_minutes', value)}
          onBlur={() => form.touch('expires_in_minutes')}
          disabled={form.submitting}
          hint={`Minutes. The platform allows up to ${MAX_CLAIM_MINUTES} (24 hours) and will silently shorten anything longer. Keep it to the window somebody is actually on site — a code that lives for a week is a site key with extra steps.`}
        />

        <InfoNote title="One code, one terminal, one use">
          <p>
            The code is shown once on the next screen and cannot be read back — the
            platform stores only a hash of it. It is consumed the moment it is
            redeemed, and it expires whether or not it is used.
          </p>
          <p>
            <strong>Issuing a second code for the same serial cancels the first.</strong>{' '}
            If somebody is already holding one for this unit, re-issuing here will strand
            them.
          </p>
        </InfoNote>

        <FormError
          message={submitErrorMessage(error)}
          requestId={error instanceof ApiError ? error.requestId : null}
        />

        <FormActions>
          <button
            type="button"
            className="button button--quiet"
            onClick={onClose}
            disabled={form.submitting}
          >
            Cancel
          </button>
          <button type="submit" className="button button--primary" disabled={form.submitting}>
            {form.submitting ? 'Issuing…' : 'Issue claim code'}
          </button>
        </FormActions>
      </form>
    </Dialog>
  )
}

/**
 * Bounds the lifetime before the server silently caps it.
 *
 * THE SERVER DOES NOT REFUSE AN OVERLONG TTL — it clamps to 24 hours and returns
 * success. So a console that passed 10,080 through would show an expiry an hour
 * later than the operator asked for and never explain why. Refusing here means
 * the number on screen and the number in force are the same one.
 */
function expiryError(raw: string): string | undefined {
  const trimmed = raw.trim()
  if (trimmed === '') return 'A lifetime is required.'

  const parsed = Number(trimmed)
  if (!Number.isInteger(parsed)) return 'The lifetime must be a whole number of minutes.'
  if (parsed < 1) return 'The lifetime must be at least one minute.'
  if (parsed > MAX_CLAIM_MINUTES) {
    return `The platform caps a claim code at ${MAX_CLAIM_MINUTES} minutes (24 hours).`
  }
  return undefined
}
