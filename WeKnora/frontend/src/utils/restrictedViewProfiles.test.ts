import assert from 'node:assert/strict'
import test from 'node:test'
import { eligibleProfileMembers, restrictedViewProfilesDraft, validateRestrictedViewProfiles, type RestrictedViewProfile } from './restrictedViewProfiles'

const profile = (): RestrictedViewProfile => ({ id: 'group-a', name: '研究组', modules: ['diagram'], member_ids: ['reader', 'other'], all_members: false })
test('legacy selected pages become one explicit group while an empty profile list stays revoked', () => {
  assert.deepEqual(restrictedViewProfilesDraft(undefined, ['diagram']), [{ id: 'previous-sharing', name: '原有页面配置', modules: ['diagram'], member_ids: [], all_members: true }])
  assert.deepEqual(restrictedViewProfilesDraft([], ['diagram']), [])
})
test('profile editing never mutates the saved member or module arrays', () => {
  const saved = profile(), draft = restrictedViewProfilesDraft([saved], [])
  draft[0]!.modules.push('market'); draft[0]!.member_ids.pop()
  assert.deepEqual(saved, profile())
})
test('removing workbench access also removes that member from every selected-member profile', () => {
  const profiles = [profile(), { ...profile(), id: 'group-b', modules: ['market'] as RestrictedViewProfile['modules'] }]
  const result = eligibleProfileMembers(profiles, { space_permission: 'none', members: [{ user_id: 'reader', permission: 'viewer' }] })
  assert.deepEqual(result.map(group => group.member_ids), [['reader'], ['reader']])
  assert.deepEqual(profiles[0]!.member_ids, ['reader', 'other'])
  assert.deepEqual(eligibleProfileMembers(profiles, { space_permission: 'viewer', members: [] })[0]!.member_ids, ['reader', 'other'])
})
test('all-member profiles do not send hidden selected-member grants and drafts need named pages and recipients', () => {
  assert.deepEqual(eligibleProfileMembers([{ ...profile(), all_members: true }], { space_permission: 'viewer', members: [] })[0]!.member_ids, [])
  assert.equal(validateRestrictedViewProfiles([profile()]), '')
  assert.match(validateRestrictedViewProfiles([{ ...profile(), modules: [] }]), /页面/)
  assert.match(validateRestrictedViewProfiles([{ ...profile(), name: ' ' }]), /名称/)
  assert.equal(validateRestrictedViewProfiles([{ ...profile(), member_ids: [] }]), '')
  assert.deepEqual(restrictedViewProfilesDraft([{ ...profile(), member_ids: null as any }], [])[0]!.member_ids, [])
  assert.match(validateRestrictedViewProfiles([profile(), profile()]), /重复/)
})
