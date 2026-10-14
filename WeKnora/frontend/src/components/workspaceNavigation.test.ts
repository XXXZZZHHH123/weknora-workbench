import assert from 'node:assert/strict'
import test from 'node:test'
import { buildWorkspaceNavigation } from './workspaceNavigation.ts'

test('global lists show real resources and send knowledge bases and workbenches to their preview', () => {
  const result = buildWorkspaceNavigation(
    [{ id: 'project-a', name: '项目 A' }],
    [{ id: 'kb-a', name: '知识库 A' }],
    [{ id: 'wb-a', name: '工作台 A' }],
  )
  assert.equal(result.projects[0].path, '/platform/projects/project-a')
  assert.equal(result.knowledgeBases[0].path, '/platform/knowledge-bases/kb-a/preview')
  assert.equal(result.workbenches[0].path, '/platform/research-workbenches/wb-a/preview')
})

test('nested API fields do not add deeper global navigation or filter unconfigured knowledge bases', () => {
  const kb = { id: 'empty-kb', name: '空知识库', folders: [{ name: '目录' }], embedding_model_id: '' }
  const project = { id: 'project', name: '项目', sessions: [{ id: 'chat' }] }
  const wb = { id: 'workbench', name: '工作台', functions: ['indicators'] }
  const result = buildWorkspaceNavigation([project], [kb], [wb])
  for (const leaves of Object.values(result)) {
    assert.equal(leaves.length, 1)
    assert.deepEqual(Object.keys(leaves[0]).sort(), ['id', 'path', 'title'])
  }
})

test('empty lists do not invent projects, knowledge bases or workbenches', () => {
  assert.deepEqual(buildWorkspaceNavigation([], [], []), { projects: [], knowledgeBases: [], workbenches: [] })
})

test('duplicate and empty identifiers are removed without hiding differently named real resources', () => {
  const resources = [
    { id: 'one', name: '同名' }, { id: 'one', name: '重复项' },
    { id: '', name: '无效项' }, { id: 'two', name: '同名' },
  ]
  assert.deepEqual(buildWorkspaceNavigation(resources, [], []).projects.map((item) => item.id), ['one', 'two'])
})

test('resource identifiers cannot inject extra path segments or query arguments', () => {
  const id = 'a/b?redirect=elsewhere#anchor'
  const result = buildWorkspaceNavigation([], [{ id, name: '<script>title</script>' }], [])
  assert.equal(result.knowledgeBases[0].path, `/platform/knowledge-bases/${encodeURIComponent(id)}/preview`)
  assert.equal(result.knowledgeBases[0].title, '<script>title</script>')
})
