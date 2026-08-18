import { useState } from 'react'

import { ApiError } from '../../api/client'
import type { TerminalDetail, WifiRecoveryStatus } from '../../api/types'
import { Dialog } from '../../components/Dialog'
import { InfoNote } from '../../components/states'
import { Timestamp } from '../../components/Timestamp'
import { useRequestWifiRecovery, useWifiRecoveryStatus } from '../../data/console'

/**
 * Change Wi-Fi.
 *
 * WHAT THE CUSTOMER IS ACTUALLY DOING. Their Wi-Fi password changed, or the
 * router was replaced, and a terminal bolted to a door has no keypad and no
 * screen to type the new one into. This asks the terminal to go back to the same
 * setup portal a brand-new unit uses; somebody standing next to it then connects
 * a phone and joins it to the new network. The console never sees a network name
 * or a password, and there is nowhere in this dialog to type one.
 *
 * THE HARDEST THING HERE IS NOT LYING, and it is harder than it looks because
 * the honest answer is uncomfortable: pressing Continue does not change any
 * Wi-Fi. It queues a command that a terminal collects on its own schedule. So
 * this screen shows three separate facts as three separate states — waiting for
 * the terminal, the terminal has it, the terminal acknowledged it — and only the
 * last one says anything happened. A dialog that showed a tick on a successful
 * POST would be reporting the console's own request back to the operator as
 * though it were the door's answer.
 *
 * AND THE COMMON CASE IS THE AWKWARD ONE. A terminal whose Wi-Fi is broken is
 * OFFLINE, which is precisely why somebody is here — and it is exactly the
 * terminal this command cannot reach. That is not an error to apologise for; it
 * is the answer, and the answer is the terminal's own local recovery. The server
 * refuses rather than queueing something that would arrive after the customer
 * had already fixed it by hand and wipe the network they had just joined it to.
 */

export function ChangeWifiDialog({
  open,
  terminal,
  onClose,
}: {
  open: boolean
  terminal: TerminalDetail
  onClose: () => void
}) {
  const request = useRequestWifiRecovery(terminal.serial_number)
  const [sent, setSent] = useState(false)

  // Polling starts only once a command has been sent from this dialog. Reading
  // the status on open would show the OUTCOME OF A PREVIOUS command beside a
  // confirmation for a new one, which is the one juxtaposition guaranteed to be
  // misread.
  const status = useWifiRecoveryStatus(terminal.serial_number, { enabled: sent })

  const name = terminal.device_name || terminal.serial_number
  const refusal = request.error instanceof ApiError ? request.error : null

  // The seeded response is authoritative the moment it arrives; the poll takes
  // over from there. Both are the same shape precisely so this is one value.
  const command: WifiRecoveryStatus | undefined = status.data ?? request.data

  return (
    <Dialog
      open={open}
      title={sent || refusal ? `Change Wi-Fi on ${name}` : 'Change Wi-Fi network?'}
      // Not dismissible mid-request: a dialog that vanished while the command
      // was in flight would leave the operator with no idea whether it went.
      dismissible={!request.isPending}
      onClose={onClose}
      description={
        sent || refusal ? undefined : (
          <>
            The terminal will temporarily enter Wi-Fi setup mode. You&apos;ll need a
            phone or computer nearby to connect it to the new Wi-Fi.
          </>
        )
      }
      footer={
        sent || refusal ? (
          <button type="button" className="button button--primary" onClick={onClose}>
            Done
          </button>
        ) : (
          <>
            <button
              type="button"
              className="button button--quiet"
              onClick={onClose}
              disabled={request.isPending}
            >
              Cancel
            </button>
            <button
              type="button"
              className="button button--primary"
              disabled={request.isPending}
              onClick={() => {
                request.mutate(undefined, {
                  // `sent` flips only on success, so a refusal leaves the
                  // dialog able to explain itself rather than showing a
                  // progress screen for a command that was never queued.
                  onSuccess: () => setSent(true),
                })
              }}
            >
              {request.isPending ? 'Working…' : 'Continue'}
            </button>
          </>
        )
      }
    >
      {refusal ? (
        <RefusedPanel error={refusal} terminal={terminal} />
      ) : sent ? (
        <ProgressPanel command={command} name={name} />
      ) : (
        <ConfirmPanel terminal={terminal} />
      )}
    </Dialog>
  )
}

// ---------------------------------------------------------------------------
// Before
// ---------------------------------------------------------------------------

/**
 * What the operator reads before pressing Continue.
 *
 * The warning for a terminal that is not currently online is shown HERE rather
 * than instead of the confirmation. The status column is the platform's last
 * report and may be a few minutes old, so the console must not refuse on its own
 * authority — the server decides, and this only sets the expectation.
 */
function ConfirmPanel({ terminal }: { terminal: TerminalDetail }) {
  return (
    <>
      <p className="confirm__detail">
        Nothing about this terminal&apos;s setup is lost. It keeps its name, its
        site, the people it recognises and their fingerprints — the only thing
        cleared is the Wi-Fi network it was joined to.
      </p>

      {terminal.status !== 'ONLINE' ? (
        <InfoNote tone="warning" title="This terminal is not online right now">
          The command can only be sent to a terminal that is currently reachable.
          If it does not respond, use the terminal&apos;s local Wi-Fi recovery
          procedure instead — hold the button on the unit for five seconds.
        </InfoNote>
      ) : null}
    </>
  )
}

// ---------------------------------------------------------------------------
// Refused
// ---------------------------------------------------------------------------

/**
 * The terminal cannot be sent a command, or the request failed outright.
 *
 * BRANCHES ON `code`, never on the message. The API says its error strings are
 * for humans and are not stable; the codes are the distinctions it has committed
 * to, and the offline one changes what the customer is told to do rather than
 * merely how it is phrased.
 */
function RefusedPanel({ error, terminal }: { error: ApiError; terminal: TerminalDetail }) {
  if (error.status === 409 && error.code === 'TERMINAL_OFFLINE') {
    return (
      <>
        <InfoNote tone="warning" title="The terminal is offline">
          The terminal is offline. Connect it to the network again or use the
          terminal&apos;s local Wi-Fi recovery procedure.
        </InfoNote>
        <LocalRecovery />
      </>
    )
  }

  if (error.status === 409 && error.code === 'TERMINAL_DISABLED') {
    return (
      <InfoNote tone="warning" title="This terminal is disabled">
        A disabled terminal does not collect commands, so nothing was sent.
        Re-enable it from this page first, then try again.
      </InfoNote>
    )
  }

  if (error.status === 409 && error.code === 'TERMINAL_CANNOT_CHANGE_WIFI') {
    return (
      <>
        <InfoNote tone="warning" title="This terminal cannot change Wi-Fi remotely">
          {error.message} Update its firmware from the firmware catalogue, then
          try again once it has checked in.
        </InfoNote>
        <LocalRecovery />
      </>
    )
  }

  if (error.status === 409 && error.code === 'TERMINAL_NOT_PROVISIONED') {
    return (
      <InfoNote tone="warning" title="This terminal has no credential">
        It has never been provisioned, or its credential was revoked, so it cannot
        collect commands. Issue a claim code for{' '}
        <code className="mono">{terminal.serial_number}</code> from{' '}
        <strong>{terminal.site_name}</strong> and redeem it at the unit.
      </InfoNote>
    )
  }

  return (
    <>
      <p className="confirm__error" role="alert">
        {error.message}
        {error.requestId ? (
          <>
            {' '}
            <span className="state__meta">
              Reference <code>{error.requestId}</code>
            </span>
          </>
        ) : null}
      </p>
      <p className="confirm__detail">
        Nothing was sent to the terminal. You can close this and try again.
      </p>
    </>
  )
}

// ---------------------------------------------------------------------------
// After
// ---------------------------------------------------------------------------

/**
 * What happened after the command was accepted for delivery.
 *
 * FOUR OUTCOMES AND FOUR DIFFERENT SENTENCES. The distinction that matters most
 * is between DELIVERED and ACCEPTED: the first means the terminal has collected
 * the command, the second means it acknowledged it — and only the second is
 * evidence that anything happened at the door.
 */
function ProgressPanel({
  command,
  name,
}: {
  command: WifiRecoveryStatus | undefined
  name: string
}) {
  if (!command) {
    return (
      <p className="confirm__detail" role="status">
        Waiting for terminal…
      </p>
    )
  }

  switch (command.state) {
    case 'ACCEPTED':
      return (
        <>
          {/*
            "ACKNOWLEDGED", NOT "IS NOW IN SETUP MODE", and the difference is not
            pedantry — it is the one thing this screen genuinely knows versus the
            one thing it is tempted to claim. The terminal acknowledges BEFORE it
            drops the link, precisely so this state is reachable at all; what
            happens after that is past anything the platform can observe.

            WHAT USED TO BE HERE, and why it is gone: an acknowledgement is
            produced by every build, including one that did not understand the
            command, so this panel carried a paragraph about old firmware that
            had acknowledged and ignored it. The server now refuses to queue the
            command for a terminal that has not reported the capability, so that
            case cannot reach this screen — a customer who gets here is on a
            build that says it can do this.

            The fallback below stays anyway, and is now the honest size: a
            terminal can still fail to reach its setup portal for reasons the
            platform never sees.
          */}
          <InfoNote title="The terminal acknowledged the command">
            {name} confirmed it received the request{' '}
            {command.acknowledged_at ? (
              <Timestamp value={command.acknowledged_at} relative />
            ) : (
              'just now'
            )}
            . It should now return to Wi-Fi setup mode.
          </InfoNote>
          <p className="confirm__detail">
            Go to the terminal with a phone or computer, connect to the setup
            network it displays, and choose the new Wi-Fi. It will come back
            online here once it has joined.
          </p>
          <p className="confirm__detail">
            <strong>If no setup network appears within a few minutes</strong>, go
            to the terminal and use the local recovery: hold the button on the
            unit for five seconds. It returns to Wi-Fi setup mode on its own.
          </p>
        </>
      )

    case 'DELIVERED':
      return (
        <>
          <p className="confirm__detail" role="status">
            The terminal has collected the command. Waiting for it to confirm…
          </p>
          <QueuedMeta command={command} />
        </>
      )

    case 'EXPIRED':
      return (
        <>
          <InfoNote tone="warning" title="The terminal never collected it">
            The command was queued but {name} did not pick it up, so it has been
            withdrawn rather than left to arrive later. Nothing changed at the
            terminal.
          </InfoNote>
          <LocalRecovery />
        </>
      )

    case 'FAILED':
      return (
        <>
          <InfoNote tone="warning" title="The terminal could not apply it">
            {name} collected the command and reported that it could not carry it
            out. Its Wi-Fi is unchanged.
          </InfoNote>
          <LocalRecovery />
        </>
      )

    case 'CANCELLED':
      return (
        <InfoNote tone="warning" title="The command was withdrawn">
          Something took {name} out of service — a revoked credential, or a
          retirement — and the command was cancelled with the rest of its queued
          work. Nothing changed at the terminal.
        </InfoNote>
      )

    // QUEUED, and NONE, which is what a status read returns if the command is
    // somehow no longer there. Both mean the same thing to the operator: it has
    // not reached the terminal.
    default:
      return (
        <>
          <p className="confirm__detail" role="status">
            Waiting for terminal…
          </p>
          <p className="confirm__detail">
            {name} collects commands when it next checks in, which is usually
            within a minute. Nothing has changed at the terminal yet.
          </p>
          <QueuedMeta command={command} />
        </>
      )
  }
}

/** The two facts worth showing while the operator waits. */
function QueuedMeta({ command }: { command: WifiRecoveryStatus }) {
  if (!command.queued_at) return null
  return (
    <p className="field__hint">
      Sent <Timestamp value={command.queued_at} relative />
      {command.already_queued ? ' — a request was already waiting, so this did not send a second one.' : ''}
    </p>
  )
}

/**
 * The way back that does not need the network, stated wherever the remote path
 * did not work.
 *
 * It is the answer to the situation this whole feature exists for: a terminal
 * whose Wi-Fi is broken cannot be told anything over Wi-Fi.
 */
function LocalRecovery() {
  return (
    <p className="confirm__detail">
      <strong>Use the terminal&apos;s local recovery instead.</strong> Hold the
      button on the unit for five seconds. It returns to Wi-Fi setup mode on its
      own, and a phone or computer nearby can then connect it to the new network.
      This does not need the terminal to be online and does not erase anything
      else.
    </p>
  )
}
