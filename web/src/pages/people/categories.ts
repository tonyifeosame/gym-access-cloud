/**
 * How a person's category READS.
 *
 * ---------------------------------------------------------------------------
 * ONLY THE PLATFORM'S OWN VALUES ARE TRANSLATED
 * ---------------------------------------------------------------------------
 *
 * `category` is FREE TEXT. The API defines no closed set: a company may store
 * "Gold member", "Year 9" or "Night shift", and the console must print those
 * back exactly as they were typed. So this is a lookup of the handful of values
 * the platform itself produces, not a formatter.
 *
 * A general `humaniseCode`-style pass would be wrong here and quietly
 * destructive: it lowercases before capitalising, so a company's "Gold Member"
 * would come back as "Gold member" -- the console editing a customer's own
 * words on a screen where they administer them.
 *
 * THE ONE THAT MATTERS MOST IS `STANDARD`. It is what the API stores when a
 * person is created without a category, so it is the common case rather than an
 * edge one, and it was rendering as a shouty code in the largest type on the
 * person page.
 */
const KNOWN: Record<string, string> = {
  STANDARD: 'Standard',
  STAFF: 'Staff',
  CONTRACTOR: 'Contractor',
  VISITOR: 'Visitor',
}

/**
 * The label for a category, or the value unchanged when it is not one of the
 * platform's own. An empty category returns an empty string; the caller decides
 * what absence looks like, because the list and the detail page say it
 * differently.
 */
export function describeCategory(category: string | undefined | null): string {
  if (!category) return ''
  return KNOWN[category.trim().toUpperCase()] ?? category
}
