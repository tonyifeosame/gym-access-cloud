import { Link } from 'react-router-dom'

/**
 * The two dead ends the console can reach.
 *
 * NEITHER IS A STATEMENT ABOUT THIS BUILD. Both of these used to render through
 * a shared "Not implemented yet" placeholder, so an operator who mistyped a URL,
 * or who opened a page their role does not cover, was told the console had not
 * been written -- which is untrue of both cases and alarming in a product
 * somebody is paying for.
 *
 * Each says what happened, whether the operator can do anything about it, and
 * offers the way back. No component here takes a `detail` prop: the message is
 * the whole point of the page, and passing one in is how the last one came to
 * say the wrong thing in two places at once.
 */

export function Forbidden() {
  return (
    <div className="page">
      <header className="page__header">
        <h1>Not available to you</h1>
        <p className="page__lead">Your role does not include this area.</p>
      </header>

      <div className="notice notice--muted">
        <p>
          If you need access, ask an owner or administrator of your company to
          change your role.
        </p>
        <p>
          <Link to="/">Back to the overview</Link>
        </p>
      </div>
    </div>
  )
}

export function NotFound() {
  return (
    <div className="page">
      <header className="page__header">
        <h1>Page not found</h1>
        <p className="page__lead">That address does not match anything in the console.</p>
      </header>

      <div className="notice notice--muted">
        <p>
          Check the address, or use the navigation to find what you were looking
          for.
        </p>
        <p>
          <Link to="/">Back to the overview</Link>
        </p>
      </div>
    </div>
  )
}
