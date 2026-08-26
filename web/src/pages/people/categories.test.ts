import { describe, expect, it } from 'vitest'

import { describeCategory } from './categories'

/**
 * A person's category.
 *
 * The property under test is a boundary: the platform's own values are the
 * console's to translate, and a company's own words are not.
 */
describe('the platform’s own categories', () => {
  it('humanises the values the platform itself stores', () => {
    expect(describeCategory('STAFF')).toBe('Staff')
    expect(describeCategory('CONTRACTOR')).toBe('Contractor')
    expect(describeCategory('VISITOR')).toBe('Visitor')
  })

  it('humanises STANDARD, which is the common case rather than an edge one', () => {
    // The API stores it when a person is created without a category, so this is
    // what most rows carry -- and it was rendering as a shouty code in the
    // largest type on the person page.
    expect(describeCategory('STANDARD')).toBe('Standard')
  })

  it('is case-insensitive and ignores surrounding space', () => {
    expect(describeCategory('staff')).toBe('Staff')
    expect(describeCategory('  Visitor  ')).toBe('Visitor')
  })
})

describe('a company’s own words', () => {
  it('leaves free text exactly as it was typed', () => {
    /*
      THE FAILURE THIS GUARDS. `category` is free text: a company may store
      "Gold Member", "Year 9" or "Night Shift". A general humanising pass
      lowercases before capitalising, so "Gold Member" would come back as
      "Gold member" -- the console quietly editing a customer's own words on
      the screen where they administer them.
    */
    expect(describeCategory('Gold Member')).toBe('Gold Member')
    expect(describeCategory('Year 9')).toBe('Year 9')
    expect(describeCategory('night shift')).toBe('night shift')
  })

  it('returns an empty string for nothing, and lets the caller phrase absence', () => {
    // The list shows an em dash; the person page says "Not set". Both are the
    // caller's decision, not this module's.
    expect(describeCategory('')).toBe('')
    expect(describeCategory(undefined)).toBe('')
    expect(describeCategory(null)).toBe('')
  })
})
