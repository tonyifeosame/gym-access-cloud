import type { BadgeTone } from './Badge'

/**
 * A proportional bar with a legend that carries the numbers.
 *
 * MOVED HERE FROM THE OVERVIEW, WHERE IT WAS PRIVATE. The Firmware screen now
 * states where the fleet stands, and it has to state it in the same shape the
 * overview does — a customer who reads "3 up to date, 1 update available" on
 * one screen and meets a differently-drawn version of the same fact on the
 * other has no way to know it is the same fact. A second implementation is how
 * the two come apart, and the way they come apart is in the arithmetic.
 *
 * THE BAR IS `aria-hidden` AND THE LEGEND IS NOT. The bar is a picture of the
 * proportions and carries no information the legend does not; announcing it
 * would give a screen-reader user a list of empty spans between the heading and
 * the numbers. The legend is an ordinary list of label/value pairs, which is
 * what the bar is actually saying.
 *
 * A ZERO SEGMENT IS DRAWN IN THE LEGEND AND NOT IN THE BAR. Zero of something
 * is a fact worth reading — "0 update available" is the good news — but a
 * zero-width slice of a bar is either invisible or, with a border, a lie.
 */
export interface MeterSegment {
  id: string
  label: string
  value: number
  tone: BadgeTone
}

export function Meter({ segments, caption }: { segments: MeterSegment[]; caption?: string }) {
  const drawn = segments.filter((segment) => segment.value > 0)

  return (
    <div className="meter">
      <div
        className={`meter__track${drawn.length === 0 ? ' meter__track--empty' : ''}`}
        aria-hidden="true"
      >
        {drawn.map((segment) => (
          <span
            key={segment.id}
            className={`meter__part meter__part--${segment.tone}`}
            style={{ flexGrow: segment.value }}
          />
        ))}
      </div>
      <ul className="legend">
        {segments.map((segment) => (
          <li key={segment.id} className="legend__item">
            <span
              className={`legend__swatch legend__swatch--${segment.tone}`}
              aria-hidden="true"
            />
            <span className="legend__label">{segment.label}</span>
            <span className="legend__value">{segment.value}</span>
          </li>
        ))}
      </ul>
      {caption ? <p className="meter__caption">{caption}</p> : null}
    </div>
  )
}
