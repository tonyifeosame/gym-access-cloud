import type { TerminalDetail, TerminalRelease } from '../../api/types'

/**
 * Which release path a terminal can actually take.
 *
 * ONE ANSWER, COMPUTED ONCE, so the dialog, the banner and the lifecycle panel
 * cannot disagree about what a unit will do with an order. Two facts decide it:
 *
 *   CAPABLE      the firmware reported `terminal_release`. Firmware that has not
 *                will ignore an order — it keeps working, keeps its roster — and
 *                the operator would wait for a confirmation that can never come.
 *                The server holds the order for such a unit and delivers it on
 *                the first check-in that reports the capability (API_SPEC 17.8),
 *                which is why the answer for old firmware is "update it", not a
 *                console procedure: the console `release` command arrived in the
 *                same firmware as the capability, so a unit without one has
 *                neither.
 *
 *   REACHABLE    the row has a credential the order can be keyed with. After a
 *   BY ORDER     revoke there is none: the order carries no MAC, the unit cannot
 *                verify it, and `order_verifiable` says so once an order exists.
 *                Before one does, the detail's `credential_active` is the same
 *                fact from the other side.
 *
 * Absence of either fact is read the pessimistic way for the capability
 * (nothing may be inferred from a missing list) and the optimistic way for the
 * credential (a console predating the health block must not refuse every
 * release; the server's `order_verifiable` corrects it the moment an order is
 * placed).
 */
export type ReleasePath =
  /** Capable firmware, live credential: the order reaches it and it wipes itself. */
  | 'automated'
  /** Live credential, old firmware: the order is held until it runs a build that can act. */
  | 'update-firmware'
  /** Capable firmware, no credential: only the unit's own console can wipe it. */
  | 'wipe-at-unit'
  /** No credential and old firmware: it can be neither told nor updated. */
  | 'no-remote-path'

export function terminalCanRelease(terminal: TerminalDetail): boolean {
  return terminal.capabilities?.includes('terminal_release') ?? false
}

export function releasePathFor(
  terminal: TerminalDetail,
  release: TerminalRelease | undefined,
): ReleasePath {
  const capable = release?.terminal_capable ?? terminalCanRelease(terminal)
  const verifiable =
    release?.state === 'ORDERED'
      ? release.order_verifiable
      : (terminal.health?.credential_active ?? true)

  if (verifiable) return capable ? 'automated' : 'update-firmware'
  return capable ? 'wipe-at-unit' : 'no-remote-path'
}

/** Whether the path ends with the unit erasing itself on the order. */
export function pathIsAutomated(path: ReleasePath): boolean {
  return path === 'automated'
}
