import assert from 'node:assert/strict'
import test from 'node:test'
import { canEditResource, canManageResource, resourcePolicyDraft, setResourceMemberPermission } from './resourcePermissions'

test('new private policy stays private while legacy draft retains the former audience', () => {
  assert.deepEqual(resourcePolicyDraft({ space_permission: 'none', members: [] }), { space_permission: 'none', members: [] })
  assert.deepEqual(resourcePolicyDraft(null), { space_permission: 'viewer', members: [] })
})
test('policy changes replace one grant, do not mutate the server snapshot or accumulate duplicates', () => {
  const saved = { space_permission: 'none' as const, members: [{ user_id: 'member', permission: 'viewer' as const }] }
  const draft = resourcePolicyDraft(saved)
  const edited = setResourceMemberPermission(draft, 'member', 'editor', 'contributor')
  assert.deepEqual(edited.members, [{ user_id: 'member', permission: 'editor' }])
  assert.deepEqual(saved.members, [{ user_id: 'member', permission: 'viewer' }])
  assert.deepEqual(setResourceMemberPermission(edited, 'member', 'none', 'contributor').members, [])
})
test('space viewers cannot receive an edit grant and empty identities are rejected', () => {
  const draft = { space_permission: 'none' as const, members: [] }
  assert.equal(setResourceMemberPermission(draft, 'reader', 'editor', 'viewer'), draft)
  assert.equal(setResourceMemberPermission(draft, '', 'viewer', 'contributor'), draft)
  assert.deepEqual(setResourceMemberPermission(draft, 'reader', 'viewer', 'viewer').members, [{ user_id: 'reader', permission: 'viewer' }])
})
test('explicit server denies outrank former creator or admin grants', () => {
  assert.equal(canEditResource({ can_edit: false, my_permission: 'admin' }), false)
  assert.equal(canManageResource({ can_manage: false, my_permission: 'admin' }), false)
  assert.equal(canEditResource({ my_permission: 'editor' }), true)
  assert.equal(canManageResource({ my_permission: 'editor' }), false)
  assert.equal(canManageResource(null), false)
})
