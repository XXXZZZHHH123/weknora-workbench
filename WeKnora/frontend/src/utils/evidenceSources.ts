import type { EvidenceDetail, EvidenceReference, EvidenceUnit } from '../api/chat/evidence'
import { parseSourceLocators, validSourceBox, type SourceLocator, type SourceLocateRequest } from './sourceLocator'

/** Never resolve by title, array position, numerical equality or another reply. */
export function findEvidenceReference(refs: EvidenceReference[] | undefined, id: string): EvidenceReference | undefined {
  return refs?.find(ref => ref.id === id)
}

export function evidenceSourceLocate(detail: EvidenceDetail, sourceIndex: number, token: number): SourceLocateRequest {
  const source = detail.sources?.[sourceIndex]
  if (detail.status !== 'snapshot' || !source) return { locators: [], quotes: [], token, unavailable: true }
  const cells = (detail.manifest?.source_cells || []).filter(cell => cell.source_sha256 === source.source_sha256)
  const locators: SourceLocator[] = []
  for (const cell of cells) {
    // PDF page ordinals are already physical and 1-based. Raw table row/column
    // indices are 0-based; they are NOT PDF page positions or invented boxes.
    if (Number.isSafeInteger(cell.page) && Number(cell.page) > 0) {
      const bbox = cell.origin !== 'manual_transcription' && validSourceBox(cell.bbox) ? cell.bbox : validSourceBox(cell.fragment_bbox) ? cell.fragment_bbox : undefined
      locators.push({ type: 'pdf', mapping: 'exact', page: Number(cell.page), source_hash: source.source_sha256,
        ...(bbox ? { bbox: bbox as number[] } : {}) })
    }
  }
  if (!locators.length) locators.push(...parseSourceLocators(source.locators))
  // A top-level locator is unambiguous only for one actual source. Never lend
  // another document's coordinates to a same-page/multi-source result.
  if (!locators.length && detail.sources?.length === 1) locators.push(...parseSourceLocators(detail.locators))
  return { locators, quotes: [], token, unavailable: false }
}

export function evidenceCellPosition(cell: Record<string, unknown>): 'cell' | 'region' | 'page' | 'unavailable' {
  if (cell.origin !== 'manual_transcription' && validSourceBox(cell.bbox)) return 'cell'
  if (validSourceBox(cell.fragment_bbox)) return 'region'
  return Number.isSafeInteger(cell.page) && Number(cell.page) > 0 ? 'page' : 'unavailable'
}

export function evidenceTableColumns(detail: EvidenceDetail): string[] {
  return evidenceTableColumnInfo(detail).map(column => column.label)
}

/** Metadata is already in actual selected-column order. A chart column's
 * original index may differ from its displayed position after projection. */
export function evidenceTableColumnInfo(detail: EvidenceDetail): Array<{ name: string; label: string; unit?: EvidenceUnit }> {
  return (detail.manifest?.result?.columns || []).map((column, index) => {
    const name = typeof column === 'string' ? column : column.name || ''
    const metadata = detail.manifest?.column_metadata?.[index]
    const sameColumn = !!name && metadata?.name === name
    return {
      name,
      label: sameColumn && metadata?.display_name ? metadata.display_name : typeof column === 'string' ? column : column.display_name || name,
      ...(sameColumn && metadata?.unit_metadata ? { unit: metadata.unit_metadata } : {}),
    }
  })
}

/** Unknown/mixed declarations never acquire a unit from an optional string. */
export function evidenceUnitSymbol(unit?: EvidenceUnit): string {
  return unit?.status === 'declared' || unit?.status === 'derived' ? unit.symbol || '' : ''
}
