export interface ResearchArtifact { id: string; title: string; body?: string; source_ids?: string[]; status?: string; revision?: number; [key: string]: unknown }
export function structureBody(text: string, parentId = '') { return JSON.stringify({ schema: 'research-structure-node-v1', text, parent_id: parentId }) }
export function structureDetails(record: ResearchArtifact): { text: string; parent_id: string } {
  try { const parsed = JSON.parse(record.body || ''); if (parsed.schema === 'research-structure-node-v1') return { text: typeof parsed.text === 'string' ? parsed.text : '', parent_id: typeof parsed.parent_id === 'string' ? parsed.parent_id : '' } } catch { /* Ordinary text remains a valid node description. */ }
  return { text: record.body || '', parent_id: '' }
}
export function structureTree(records: ResearchArtifact[]) {
  const byId = new Map(records.map(record => [record.id, record]))
  function depth(record: ResearchArtifact) {
    const seen = new Set([record.id]); let count = 0, parent = structureDetails(record).parent_id
    while (parent && byId.has(parent) && !seen.has(parent) && count < 12) { seen.add(parent); count++; parent = structureDetails(byId.get(parent)!).parent_id }
    return count
  }
  return records.map(record => ({ ...record, ...structureDetails(record), depth: depth(record) }))
}
export function observationChart(observations: Array<{ value?: unknown; as_of?: string }>, width = 580, height = 150) {
  const rows = observations.map((row, index) => ({ value: typeof row.value === 'number' && Number.isFinite(row.value) ? row.value : null, time: Date.parse(row.as_of || ''), index }))
  const finite = rows.filter(row => row.value !== null)
  if (!finite.length) return { segments: [] as string[], min: null, max: null }
  const min = Math.min(...finite.map(row => row.value!)), max = Math.max(...finite.map(row => row.value!))
  const allDates = rows.every(row => Number.isFinite(row.time))
  const xs = rows.map(row => allDates ? row.time : row.index), first = Math.min(...xs), last = Math.max(...xs)
  const segments: string[] = []; let current: string[] = []
  rows.forEach((row, index) => {
    if (row.value === null) { if (current.length) segments.push(current.join(' ')); current = []; return }
    const x = 10 + ((xs[index]! - first) / (last - first || 1)) * (width - 20)
    const y = 10 + (max === min ? .5 : (max - row.value) / (max - min)) * (height - 20)
    current.push(`${x.toFixed(2)},${y.toFixed(2)}`)
  })
  if (current.length) segments.push(current.join(' '))
  return { segments, min, max }
}
export function reusableTemplate(templateId: string, modules: readonly string[], structure: ResearchArtifact[]) {
  const keys = new Map(structure.map((record, index) => [record.id, `node_${index + 1}`]))
  return { schema: 'research-workbench-template-v1', template_id: templateId, template_version: 1,
    modules: [...modules], structure_slots: structure.map(record => ({ key: keys.get(record.id), parent_key: keys.get(structureDetails(record).parent_id) || null, title: '' })),
    evidence_requirements: { observations_require_source: true, conclusions_require_confirmation: true },
  }
}
