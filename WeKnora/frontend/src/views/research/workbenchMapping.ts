export interface PreviewCell { cell: string; value: unknown }
export interface MappingColumn { column: string; metric: string; unit?: string | null; frequency?: string; frequency_cells?: string[]; header_cells?: string[]; [key: string]: unknown }
export interface MappingBlock { header_rows: number[]; columns: MappingColumn[]; [key: string]: unknown }
export function parseHeaderRows(input: string): number[] {
  const tokens = input.trim().split(/[，,\s]+/)
  if (!tokens.length || tokens.some(token => !/^[1-9]\d*$/.test(token))) throw new Error('表头行请用正整数填写，以逗号分隔。')
  return [...new Set(tokens.map(Number))]
}
export function applyMetadataRows(block: MappingBlock, preview: PreviewCell[][], nameRow?: number, unitRow?: number, frequencyRow?: number) {
  if (!nameRow && !unitRow && !frequencyRow) throw new Error('先明确选择名称行、单位行或频率行，再应用。')
  for (const row of [nameRow,unitRow,frequencyRow]) if (row && !block.header_rows.includes(row)) throw new Error('所选元数据行必须在已声明的表头范围内。')
  const cells = new Map(preview.flat().map(cell => [cell.cell,cell.value]))
  const next = block.columns.map(column => {
    const item = {...column,header_cells:[...(column.header_cells||[])]}
    if (nameRow) {
      const key=`${column.column}${nameRow}`,value=cells.get(key)
      if (!cells.has(key) || value === null || value === undefined || !String(value).trim()) throw new Error(`${key} 没有可用名称，请对照原件单独填写。`)
      item.metric=String(value).trim();item.header_cells.push(key)
    }
    if (unitRow) {
      const key=`${column.column}${unitRow}`
      if (!cells.has(key)) throw new Error(`${key} 不在已读取的原件预览中，请读取对应区域后再确认。`)
      const value=cells.get(key)
      item.unit=value === null || value === undefined || !String(value).trim()?null:String(value).trim()
      item.header_cells.push(key)
    }
    if (frequencyRow) {
      const key=`${column.column}${frequencyRow}`,value=cells.get(key)
      if (!cells.has(key) || value === null || value === undefined || !String(value).trim()) throw new Error(`${key} 没有频率原文，请保留待核或单独校正。`)
      item.frequency=String(value).trim();item.frequency_cells=[key]
      item.header_cells.push(key)
    }
    item.header_cells=[...new Set(item.header_cells)]
    return item
  })
  return {...block,columns:next}
}
