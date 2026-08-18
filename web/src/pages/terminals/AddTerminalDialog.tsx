import { useEffect, useId, useMemo, useRef, useState } from 'react'

import { ApiError } from '../../api/client'
import {
  PAIRING_CODE_GROUP,
  PAIRING_CODE_LENGTH,
  type PendingTerminal,
  type Site,
} from '../../api/types'
import { Dialog } from '../../components/Dialog'
import { FormActions, FormError, SelectField, TextField } from '../../components/Form'
import { useNotifications } from '../../components/Notifications'
import { InfoNote } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { submitErrorMessage, useForm, validators } from '../../components/useForm'
import { useAdoptTerminal, useApproveTerminal, useSites } from '../../data/console'

/**
 * Adding a terminal, the way a customer does it.
 *
 * THE WHOLE FLOW IS: power the unit on, put it on Wi-Fi from a phone, read the
 * eight characters off its screen, type them here, pick a site, approve. The
 * customer never sees a serial number, a URL, a provisioning key or a cable —
 * and this dialog is where that promise is either kept or broken, so the first
 * screen asks for exactly one thing and explains where to find it.
 *
 * WHAT THIS IS NOT, because the distinction is load-bearing and easy to lose:
 * the code typed here is NOT a credential the platform issued to somebody. It is
 * displayed by the hardware and it binds one announcement from one serial.
 * Nothing this dialog sends can be replayed to provision different hardware, and
 * nothing it receives is a secret — which is why, unlike the claim-code dialog,
 * there is no one-time panel here and no warning about copying anything down.
 *
 * TWO STEPS, ONE DIALOG, and they cannot be separated for the same reason the
 * claim-code dialog's cannot: step one produces the identifier step two acts on,
 * and a customer who closed in between would have adopted a terminal they can no
 * longer find. Closing between them is survivable here in a way it is not there
 * — the terminal is on the Waiting to be set up list and can be approved from it
 * — and the dialog says so rather than trapping anybody.
 */

// ---------------------------------------------------------------------------
// Formatting what somebody types
// ---------------------------------------------------------------------------

/**
 * Turns anything a customer types or pastes into the canonical XXXX-XXXX.
 *
 * UPPER-CASED, because the panel shows upper case and the server normalises the
 * same way — somebody typing in lower case must not be refused for a reason
 * nobody could guess. Separators are re-derived rather than preserved, so a
 * pasted `k7m2 p4qx`, `K7M2P4QX` and `k7m2-p4qx` are one value.
 *
 * DELIBERATELY NOT A VALIDATOR. Characters outside the server's alphabet are
 * kept rather than silently dropped: a customer who misreads B for 8 should be
 * told the code was not recognised, not watch the letter vanish as they type and
 * be left with a code that looks right and is short.
 */
export function formatPairingCode(raw: string): string {
  const bare = raw
    .toUpperCase()
    .replace(/[^A-Z0-9]/g, '')
    .slice(0, PAIRING_CODE_LENGTH)

  if (bare.length <= PAIRING_CODE_GROUP) return bare
  return `${bare.slice(0, PAIRING_CODE_GROUP)}-${bare.slice(PAIRING_CODE_GROUP)}`
}

// ---------------------------------------------------------------------------
// Step 1 — the code
// ---------------------------------------------------------------------------

interface CodeValues extends Record<string, unknown> {
  pairing_code: string
}

function EnterCodeStep({
  onAdopted,
  onCancel,
}: {
  onAdopted: (pending: PendingTerminal) => void
  onCancel: () => void
}) {
  const adopt = useAdoptTerminal()

  const form = useForm<CodeValues>({
    initialValues: { pairing_code: '' },
    validate: (values) => ({
      pairing_code:
        validators.required(values.pairing_code, 'The code from the terminal') ??
        (values.pairing_code.replace(/-/g, '').length < PAIRING_CODE_LENGTH
          ? `The code is ${PAIRING_CODE_LENGTH} characters. Check the terminal's screen.`
          : undefined),
    }),
    onSubmit: async (values) => {
      onAdopted(await adopt.mutateAsync({ pairing_code: values.pairing_code.trim() }))
    },
  })

  const error = form.submitError

  return (
    <form className="form" onSubmit={(event) => void form.handleSubmit(event)} noValidate>
      {/*
        THE THREE STEPS COME BEFORE THE FIELD, not after it. Somebody opening
        this has a box in front of them and has not done any of it yet; a field
        with the instructions underneath asks them to type something they have
        not got.
      */}
      <ol className="steps">
        <li>
          <strong>Power the terminal on</strong> and wait for its screen to light up.
        </li>
        <li>
          <strong>Connect it to Wi-Fi</strong> from a phone or laptop. The terminal
          starts its own temporary network called <code className="mono">AccessLink-</code>
          followed by four characters, and shows the password for it on its screen.
        </li>
        <li>
          <strong>Read the code</strong> the terminal then shows — eight characters,
          like <code className="mono">K7M2-P4QX</code> — and type it below.
        </li>
      </ol>

      <TextField
        label="Code from the terminal"
        required
        mono
        value={form.values.pairing_code}
        error={form.errors.pairing_code}
        // Formatted as they type: upper-cased and hyphenated, so what is on
        // screen matches what is on the panel without anybody being careful.
        onChange={(value) => form.setValue('pairing_code', formatPairingCode(value))}
        onBlur={() => form.touch('pairing_code')}
        disabled={form.submitting}
        hint="The terminal shows this on its own screen once it is on Wi-Fi, on firmware 1.2.0 or newer. It changes every 15 minutes — if it has, just read the new one."
      />

      <PairingCodeError error={error} />

      <FormActions>
        <button
          type="button"
          className="button button--quiet"
          onClick={onCancel}
          disabled={form.submitting}
        >
          Cancel
        </button>
        <button type="submit" className="button button--primary" disabled={form.submitting}>
          {form.submitting ? 'Checking…' : 'Continue'}
        </button>
      </FormActions>
    </form>
  )
}

/**
 * The three refusals, told apart by STATUS rather than by parsing a message.
 *
 * The API's error strings are explicitly not stable, so the shape of the panel
 * is chosen by the status code and the server's sentence is shown inside it —
 * the same rule the capacity refusal follows, and for the same reason: on a
 * conflict the server's wording IS the actionable part.
 */
function PairingCodeError({ error }: { error: unknown }) {
  if (!(error instanceof ApiError)) {
    return <FormError message={submitErrorMessage(error)} requestId={null} />
  }

  if (error.status === 404) {
    return (
      <div className="notice notice--warning" role="alert">
        <p>
          <strong>That code was not recognised.</strong>
        </p>
        <p>
          Codes last 15 minutes. Check the terminal's screen for the one it is showing
          now — if it has changed, type the new one. Make sure the terminal is still
          connected to Wi-Fi.
        </p>
      </div>
    )
  }

  if (error.status === 409) {
    return (
      <div className="notice notice--danger" role="alert">
        <p>
          <strong>This terminal cannot be added here.</strong>
        </p>
        <p>{error.message}</p>
        <p className="muted">
          If this hardware is yours and was previously used by another AccessLink
          account, it has to be released by AccessLink support before it can be set
          up again. That is deliberate: a terminal is never moved between accounts
          without both sides agreeing.
        </p>
      </div>
    )
  }

  return (
    <FormError message={submitErrorMessage(error)} requestId={error.requestId} />
  )
}

// ---------------------------------------------------------------------------
// Step 2 — confirm the hardware, place it, approve
// ---------------------------------------------------------------------------

interface PlaceValues extends Record<string, unknown> {
  site_id: string
  device_name: string
}

export function ConfirmTerminalStep({
  pending,
  onApproved,
  onCancel,
}: {
  pending: PendingTerminal
  onApproved: (approved: PendingTerminal) => void
  onCancel: () => void
}) {
  const sites = useSites()
  const approve = useApproveTerminal(pending.id)
  const notifications = useNotifications()
  const headingRef = useRef<HTMLHeadingElement | null>(null)
  const summaryId = useId()

  // Focus the heading when the step changes: without it a keyboard user is left
  // on the button that submitted the previous step, with the whole confirmation
  // panel above them unannounced.
  useEffect(() => {
    headingRef.current?.focus()
  }, [])

  // Active sites only. The server refuses a deactivated one, so offering it here
  // would be an option that fails on submit for a reason the list did not show.
  const options = useMemo(
    () =>
      (sites.data?.sites ?? [])
        .filter((site: Site) => site.active)
        .map((site: Site) => ({ value: site.id, label: site.name })),
    [sites.data],
  )

  const refused =
    pending.verdict === 'REFUSED_OTHER_COMPANY' || pending.verdict === 'REFUSED_DISABLED'

  // Pre-selected when there is exactly one site, which is every new customer:
  // signup creates Main Site, so their first terminal needs no choice made at
  // all.
  const onlySiteId = options.length === 1 ? options[0]!.value : ''

  const form = useForm<PlaceValues>({
    initialValues: {
      site_id: onlySiteId,
      device_name: '',
    },
    validate: (values) => ({
      site_id: validators.required(values.site_id, 'A site'),
    }),
    onSubmit: async (values) => {
      const approved = await approve.mutateAsync({
        site_id: values.site_id,
        device_name: values.device_name.trim(),
      })
      notifications.success(
        `${approved.device_name || approved.serial_number} is being set up. It will show its name on screen shortly.`,
      )
      onApproved(approved)
    },
  })

  // The single site cannot be chosen before `sites` has loaded, so apply it when
  // it arrives rather than leaving the field empty and the customer picking the
  // only option there is.
  useEffect(() => {
    if (!form.values.site_id && onlySiteId) {
      form.setValue('site_id', onlySiteId)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [onlySiteId])

  return (
    <form className="form" onSubmit={(event) => void form.handleSubmit(event)} noValidate>
      <section className="panel" aria-describedby={summaryId}>
        <h3 className="panel__title" tabIndex={-1} ref={headingRef}>
          Is this the terminal in front of you?
        </h3>

        {/*
          THE CORROBORATION. A customer is being asked to take responsibility for
          a piece of hardware, and these four facts are how they check it is
          theirs rather than a code somebody read to them over the phone.
        */}
        <dl className="detail-list" id={summaryId}>
          <div className="detail-list__row">
            <dt>Serial number</dt>
            <dd>
              <code className="mono">{pending.serial_number}</code>
            </dd>
          </div>
          <div className="detail-list__row">
            <dt>Hardware</dt>
            <dd>{pending.hardware_revision || <span className="muted">Not reported</span>}</dd>
          </div>
          <div className="detail-list__row">
            <dt>Firmware</dt>
            <dd>
              {pending.firmware_version ? (
                <code className="mono">{pending.firmware_version}</code>
              ) : (
                <span className="muted">Not reported</span>
              )}
            </dd>
          </div>
          {/*
            WHAT IT CAN DO, and the three answers are genuinely different.
            "Not reported" is the whole fleet built before capability reporting
            AND a brand-new unit that has only just announced; "None" is a build
            that reports and has none. An administrator deciding whether to bolt
            this to a door wants to know whether it can be recovered over the
            network before they do, not after.
          */}
          <div className="detail-list__row">
            <dt>Can be set up remotely</dt>
            <dd>
              {pending.capabilities === undefined ? (
                <span className="muted">Not reported</span>
              ) : pending.capabilities.includes('wifi_recovery') ? (
                'Yes — Wi-Fi can be changed from here'
              ) : (
                'No — Wi-Fi changes need somebody at the terminal'
              )}
            </dd>
          </div>
          <div className="detail-list__row">
            <dt>Last seen</dt>
            <dd>
              {pending.last_seen_at ? (
                <Timestamp value={pending.last_seen_at} relative />
              ) : (
                <Timestamp value={pending.announced_at} relative />
              )}
              {pending.first_seen_ip ? (
                <span className="muted"> from {pending.first_seen_ip}</span>
              ) : null}
            </dd>
          </div>
        </dl>
      </section>

      {pending.verdict === 'RE_PROVISION' && pending.existing_terminal ? (
        <div className="notice notice--warning" role="alert">
          <p>
            <strong>
              This terminal is already set up as{' '}
              {pending.existing_terminal.device_name || pending.existing_terminal.serial_number}
              {pending.existing_terminal.site_name
                ? ` at ${pending.existing_terminal.site_name}`
                : ''}
              .
            </strong>
          </p>
          <p>
            Setting it up again gives it a new credential, and{' '}
            <strong>the one it is using now stops working</strong>. That is what you
            want after a factory reset or a repair, and it is not what you want if the
            terminal is currently working — it will be offline until it picks the new
            one up.
          </p>
        </div>
      ) : null}

      {refused ? (
        <div className="notice notice--danger" role="alert">
          <p>
            <strong>This terminal cannot be set up right now.</strong>
          </p>
          <p>
            {pending.verdict === 'REFUSED_DISABLED'
              ? 'It is disabled. Re-enable it from the terminal’s page first, then add it again.'
              : 'It is registered to another AccessLink account. It has to be released by AccessLink support before it can be set up here.'}
          </p>
        </div>
      ) : null}

      <SelectField
        label="Site"
        required
        value={form.values.site_id}
        options={options}
        error={form.errors.site_id}
        onChange={(value) => form.setValue('site_id', value)}
        onBlur={() => form.touch('site_id')}
        disabled={form.submitting || refused}
        placeholder={options.length === 1 ? undefined : 'Choose a site'}
        hint="Where this terminal is installed. It decides who the terminal will let in."
      />

      <TextField
        label="Name this terminal"
        value={form.values.device_name}
        onChange={(value) => form.setValue('device_name', value)}
        onBlur={() => form.touch('device_name')}
        disabled={form.submitting || refused}
        hint="Optional. Something you would say out loud — “Front Door”, “Staff Entrance”. The terminal shows it on its own screen once it is set up."
      />

      <InfoNote title="What happens when you approve">
        <p>
          The terminal collects its credential the next time it checks in, usually
          within a few seconds, and then shows your company and site name on its
          screen. Nothing is displayed here for you to copy — the terminal fetches
          its own credential and it is never shown to anybody.
        </p>
      </InfoNote>

      <FormError
        message={submitErrorMessage(form.submitError)}
        requestId={form.submitError instanceof ApiError ? form.submitError.requestId : null}
      />

      <FormActions>
        <button
          type="button"
          className="button button--quiet"
          onClick={onCancel}
          disabled={form.submitting}
        >
          Cancel
        </button>
        <button
          type="submit"
          className="button button--primary"
          disabled={form.submitting || refused}
        >
          {form.submitting ? 'Setting up…' : 'Approve and set up'}
        </button>
      </FormActions>
    </form>
  )
}

// ---------------------------------------------------------------------------
// Step 3 — done
// ---------------------------------------------------------------------------

function ApprovedStep({
  approved,
  onClose,
}: {
  approved: PendingTerminal
  onClose: () => void
}) {
  const headingRef = useRef<HTMLHeadingElement | null>(null)
  useEffect(() => {
    headingRef.current?.focus()
  }, [])

  return (
    <section className="panel">
      <h3 className="panel__title" tabIndex={-1} ref={headingRef}>
        {approved.device_name || approved.serial_number} is being set up
      </h3>

      <p>
        The terminal is collecting its credential now. Within a few seconds its screen
        will show <strong>{approved.site_name}</strong> and the name you gave it, and it
        will start letting people in according to your access rules.
      </p>

      {/*
        SAID PLAINLY RATHER THAN LEFT TO BE DISCOVERED. Approval is not the same
        moment as the terminal working, and a customer who walks away from the
        unit expecting it to be live is the person this sentence is for.
      */}
      <p className="muted">
        If it does not, check it is still on Wi-Fi. It stays on the{' '}
        <strong>Waiting to be set up</strong> list until it has collected, and you can
        approve it again from there.
      </p>

      <FormActions>
        <button type="button" className="button button--primary" onClick={onClose}>
          Done
        </button>
      </FormActions>
    </section>
  )
}

// ---------------------------------------------------------------------------
// The dialog
// ---------------------------------------------------------------------------

type Stage =
  | { step: 'code' }
  | { step: 'confirm'; pending: PendingTerminal }
  | { step: 'approved'; approved: PendingTerminal }

export function AddTerminalDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [stage, setStage] = useState<Stage>({ step: 'code' })

  function close() {
    setStage({ step: 'code' })
    onClose()
  }

  if (stage.step === 'confirm') {
    return (
      <Dialog
        open={open}
        title="Confirm the terminal"
        description="Check this is the unit in front of you, then choose where it is installed."
        size="wide"
        onClose={close}
      >
        <ConfirmTerminalStep
          pending={stage.pending}
          onApproved={(approved) => setStage({ step: 'approved', approved })}
          onCancel={close}
        />
      </Dialog>
    )
  }

  if (stage.step === 'approved') {
    return (
      <Dialog open={open} title="Terminal added" size="wide" onClose={close}>
        <ApprovedStep approved={stage.approved} onClose={close} />
      </Dialog>
    )
  }

  return (
    <Dialog
      open={open}
      title="Add a terminal"
      description="Terminals show a code on their own screen once they are on Wi-Fi. Type it here to add one."
      size="wide"
      onClose={close}
    >
      <EnterCodeStep
        onAdopted={(pending) => setStage({ step: 'confirm', pending })}
        onCancel={close}
      />
    </Dialog>
  )
}
