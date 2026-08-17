import { describe, expect, it } from 'vitest'

import { roleAtLeast, roleLabel } from './roles'

describe('role ordering', () => {
  it('matches the backend ordering OWNER > ADMIN > MANAGER > VIEWER', () => {
    expect(roleAtLeast('OWNER', 'VIEWER')).toBe(true)
    expect(roleAtLeast('OWNER', 'OWNER')).toBe(true)
    expect(roleAtLeast('ADMIN', 'MANAGER')).toBe(true)
    expect(roleAtLeast('MANAGER', 'VIEWER')).toBe(true)
    expect(roleAtLeast('VIEWER', 'VIEWER')).toBe(true)

    expect(roleAtLeast('VIEWER', 'MANAGER')).toBe(false)
    expect(roleAtLeast('MANAGER', 'ADMIN')).toBe(false)
    expect(roleAtLeast('ADMIN', 'OWNER')).toBe(false)
  })

  it('fails closed on anything it does not recognise', () => {
    // A build that does not know a role cannot reason about what it may do.
    expect(roleAtLeast(undefined, 'VIEWER')).toBe(false)
    expect(roleAtLeast('', 'VIEWER')).toBe(false)
    expect(roleAtLeast('SUPERUSER', 'VIEWER')).toBe(false)
    expect(roleAtLeast('viewer', 'VIEWER')).toBe(false)
  })

  it('labels roles for display and passes unknown ones through', () => {
    expect(roleLabel('OWNER')).toBe('Owner')
    expect(roleLabel('SOMETHING_NEW')).toBe('SOMETHING_NEW')
  })
})
