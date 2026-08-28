import { useEffect, useId, useRef, useState } from 'react'

/**
 * Paging and search over a server-paginated list.
 *
 * BOTH ARE SERVER-SIDE, and this component only ever reports intent. Filtering
 * or sorting a fetched page in the browser searches the page rather than the
 * data, which is silently wrong the moment a company has more people than fit on
 * one — and silently right in every test with three fixtures, which is what
 * makes it worth stating.
 */

export interface PaginationProps {
  /** Rows on this page. */
  count: number
  /** Rows in the whole match. */
  total: number
  offset: number
  limit: number
  hasMore: boolean
  onOffsetChange: (offset: number) => void
  /** Names the thing being counted: "people", "terminals". */
  noun: string
  /** True while the next page is in flight, to disable the controls. */
  busy?: boolean
}

export function Pagination({
  count,
  total,
  offset,
  limit,
  hasMore,
  onOffsetChange,
  noun,
  busy,
}: PaginationProps) {
  // An empty result has its own empty state above and needs nothing from here.
  if (total === 0) {
    return null
  }

  const first = offset + 1
  const last = offset + count
  const page = Math.floor(offset / limit) + 1
  const pages = Math.max(1, Math.ceil(total / limit))

  /*
    THE COUNT IS SHOWN EVEN WHEN THERE IS ONLY ONE PAGE; ONLY THE CONTROLS HIDE.

    This component used to return null whenever everything fitted on one page,
    and the count line lives inside it -- so "how many matched" disappeared in
    exactly the case where it is the answer. On the audit trail that is the
    normal case: an operator narrows to one person and a fortnight, and "three
    records match" IS the finding. The table showed three rows and said nothing
    about whether that was all of them.

    Prev/Next are still suppressed for a single page, because a pair of disabled
    buttons is a worse statement of "there is no more" than their absence.
  */
  const paged = pages > 1

  return (
    <nav className="pagination" aria-label={`${noun} pagination`}>
      {/* A live region: paging changes what is on screen without moving focus,
          so the range has to be announced or the change is silent. */}
      <p className="pagination__status" aria-live="polite">
        Showing <strong>{first}</strong>–<strong>{last}</strong> of <strong>{total}</strong> {noun}
      </p>

      {paged ? (
      <div className="pagination__controls">
        <button
          type="button"
          className="button button--quiet"
          onClick={() => onOffsetChange(Math.max(0, offset - limit))}
          disabled={offset === 0 || busy}
        >
          Previous
        </button>
        <span className="pagination__page">
          Page {page} of {pages}
        </span>
        <button
          type="button"
          className="button button--quiet"
          onClick={() => onOffsetChange(offset + limit)}
          disabled={!hasMore || busy}
        >
          Next
        </button>
      </div>
      ) : null}
    </nav>
  )
}

export interface SearchInputProps {
  value: string
  onChange: (value: string) => void
  label: string
  placeholder?: string
  /** Milliseconds to wait after typing stops. */
  debounceMs?: number
  busy?: boolean
}

/**
 * A debounced search box.
 *
 * Debounced because each change is a request AND a cache entry: firing on every
 * keystroke turns a six-letter name into six round trips and six cached pages.
 * The local value updates immediately so typing never feels laggy — only the
 * reported value waits.
 */
export function SearchInput({
  value,
  onChange,
  label,
  placeholder,
  debounceMs = 300,
  busy,
}: SearchInputProps) {
  const id = useId()
  const [local, setLocal] = useState(value)
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange

  // Adopt an externally cleared or restored value (a "clear filters" button, or
  // a value read back from the URL) without fighting the operator's typing.
  useEffect(() => {
    setLocal(value)
  }, [value])

  useEffect(() => {
    if (local === value) return
    const timer = setTimeout(() => onChangeRef.current(local), debounceMs)
    return () => clearTimeout(timer)
  }, [debounceMs, local, value])

  return (
    <div className="search">
      <label className="field__label" htmlFor={id}>
        {label}
      </label>
      <div className="search__control">
        <input
          id={id}
          type="search"
          className="field__input"
          value={local}
          placeholder={placeholder}
          onChange={(event) => setLocal(event.target.value)}
          autoComplete="off"
        />
        {busy ? <span className="spinner spinner--inline" aria-hidden="true" /> : null}
      </div>
    </div>
  )
}
