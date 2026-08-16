import type { FirmwareVersion, Terminal } from '../../api/types'

/**
 * Whether the platform would actually offer a build, and to whom.
 *
 * WHY THIS EXISTS AT ALL. Promoting a build is now the act that starts a
 * rollout: every heartbeat from a matching terminal is answered with a
 * `firmware_update` offer, and the device downloads, verifies, flashes and
 * reboots. But the server WITHHOLDS an offer it could not populate, logs the
 * reason against the catalogue row, and tells nobody — so a build missing a
 * digest can be promoted, look promoted, and update nothing for ever.
 *
 * That failure is invisible from the console unless the console works it out,
 * and it is the exact shape of the thing this product keeps getting wrong: a
 * screen that reports a decision was made rather than what the decision did.
 *
 * MIRRORED FROM `database/firmware_offer.go`, RULE FOR RULE. If the two ever
 * disagree the server wins, and the visible symptom is a build this page calls
 * deliverable that the fleet is never offered. Every check below names the
 * server's reason for it, because a mirror nobody can check against the original
 * rots.
 *
 * The device enforces all of these again on arrival. That is not duplication
 * worth removing: the device's checks are the ones an attacker would have to
 * defeat, and these are the ones that stop an operator waiting a week for a
 * rollout that was never going to happen.
 */

/** 64 LOWER-CASE hex. Folding case here would disagree with the server. */
const DIGEST = /^[0-9a-f]{64}$/

/** `firmware_update.h`: `char url[128]`. */
const MAX_URL_LENGTH = 127

/** `firmware_update.h`: `kFirmwareVersionSize = 24`. */
const MAX_VERSION_LENGTH = 23

export interface Offerability {
  /** True when the platform would send this build to a terminal. */
  deliverable: boolean
  /**
   * Why not, in the operator's terms and naming the field to fix.
   *
   * Every unmet rule, not the first: somebody publishing a build wants the whole
   * list, and reporting them one at a time turns one correction into four.
   */
  problems: string[]
}

export function firmwareOfferability(version: FirmwareVersion): Offerability {
  const problems: string[] = []
  const checksum = version.checksum_sha256 ?? ''
  const url = version.download_url ?? ''

  // 1 and 2. TLS proves the image came from the right server, not that it
  //          arrived intact and not that anybody signed it. The digest is the
  //          only end-to-end check the terminal has, so it refuses an offer
  //          without one.
  if (checksum === '') {
    problems.push('It has no SHA-256 checksum, and a terminal refuses an update without one.')
  } else if (!DIGEST.test(checksum)) {
    problems.push(
      'Its checksum is not 64 lower-case hexadecimal characters. The platform and the ' +
        'terminal compare it exactly, so an upper-case digest never matches.',
    )
  }

  // 3. The size the flash write is bounded by, trusted over Content-Length
  //    deliberately — a length from a compromised host is exactly the value not
  //    to trust with a flash write.
  if (version.size_bytes === undefined || version.size_bytes === null || version.size_bytes <= 0) {
    problems.push('It has no file size. The terminal uses it to size the flash write.')
  }

  // 4 and 5. An update channel is the highest-value thing to attack here, and a
  //          plaintext download is refused by the device before a socket opens.
  if (url === '') {
    problems.push('It has no download address, so there is nothing for a terminal to fetch.')
  } else if (!url.toLowerCase().startsWith('https://')) {
    problems.push('Its download address is not https. A terminal refuses a plaintext download.')
  } else if (url.length > MAX_URL_LENGTH) {
    problems.push(
      `Its download address is ${url.length} characters and a terminal can hold ` +
        `${MAX_URL_LENGTH}. A truncated address fetches the wrong image or none.`,
    )
  }

  // The device's other fixed buffer. A truncated version compares wrongly, which
  // reads as a terminal that is permanently behind.
  if (version.version.length > MAX_VERSION_LENGTH) {
    problems.push(
      `Its version string is ${version.version.length} characters and a terminal can hold ` +
        `${MAX_VERSION_LENGTH}.`,
    )
  }

  return { deliverable: problems.length === 0, problems }
}

/**
 * The terminals that would be offered this build if it became current.
 *
 * THE SERVER'S OWN NARROWING, and all three parts of it matter. The candidate is
 * the current build for the terminal's device type AND release channel — a
 * reader must not be offered a terminal image, and a terminal on STABLE must not
 * receive a CANARY build — and a terminal already running the version is left
 * alone.
 *
 * The comparison is an EXACT STRING, matching how the server decides and how
 * `firmware_outdated` is computed. The device performs the ordering comparison
 * and refuses anything not newer than what it runs; two implementations of
 * version ordering that could disagree would be a worse problem than this
 * comparison being coarse.
 *
 * The list handed in is the operator's own scoped fleet, so the answer is
 * "terminals you can see" and never the company's total. A number that looks
 * fleet-wide and is not would be worse than no number.
 */
export function terminalsOffered(
  version: Pick<FirmwareVersion, 'version' | 'device_type' | 'release_channel'>,
  terminals: Terminal[],
): Terminal[] {
  return terminals.filter(
    (terminal) =>
      terminal.device_type === version.device_type &&
      terminal.release_channel === version.release_channel &&
      terminal.firmware_version !== version.version,
  )
}
