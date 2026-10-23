import assert from 'node:assert/strict'
import test from 'node:test'
import { tenantMemberAccess } from './tenantMemberAccess'

test('administrators can manage non-owner members and cannot assign owner', () => {
  const access = tenantMemberAccess('admin')
  assert.equal(access.canManage, true)
  assert.equal(access.canManageOwners, false)
  assert.deepEqual(access.assignableRoles, ['admin', 'contributor', 'viewer'])
  for (const role of ['admin', 'contributor', 'viewer'] as const) {
    assert.equal(access.canManageMember({ user_id: 'other', role }, 'self'), true)
  }
  assert.equal(access.canManageMember({ user_id: 'other', role: 'owner' }, 'self'), false)
  assert.equal(access.canManageMember({ user_id: 'self', role: 'admin' }, 'self'), false)
})

test('owners and authorized cross-tenant operators retain ownership management', () => {
  for (const access of [tenantMemberAccess('owner'), tenantMemberAccess('admin', true)]) {
    assert.equal(access.canManageOwners, true)
    assert.equal(access.canManageMember({ user_id: 'other', role: 'owner' }, 'self'), true)
    assert.deepEqual(access.assignableRoles, ['owner', 'admin', 'contributor', 'viewer'])
  }
})

test('contributors, viewers, and unresolved roles cannot manage members', () => {
  for (const role of ['contributor', 'viewer', ''] as const) {
    const access = tenantMemberAccess(role)
    assert.equal(access.canManage, false)
    assert.deepEqual(access.assignableRoles, [])
    assert.equal(access.canManageMember({ user_id: 'other', role: 'viewer' }, 'self'), false)
  }
})
