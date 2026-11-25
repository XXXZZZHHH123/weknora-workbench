"""Apply compact AI edits while preserving columns omitted from the response."""
from copy import deepcopy


BLOCK_FIELDS = {'name', 'date_mode', 'date_column', 'date_cell', 'date_format',
                'category_column', 'category_name', 'row_label_column',
                'header_rows', 'header_note_cells', 'data_start_row', 'data_end_row', 'row_rules'}
COLUMN_FIELDS = {'metric', 'unit', 'dimensions', 'period', 'header_cells',
                 'frequency', 'frequency_cells', 'unit_cells', 'field_role'}

HEADER_NOTES_PROMPT = '''请由模型根据原单元格的实际内容及相邻表头的关系，判断该单元格的业务用途，不按字面关键词或格式判断。
表头中的整段说明、备注、数据来源和口径说明等，若解释的是表格或指标而非指标名称，把真实坐标放入相应区块的 header_note_cells；同一区块任何列的 header_cells、metric、dimensions 不得使用这些说明作为名称或维度。指标名称由实际名称、代码等识别行组成；单位继续用 unit/unit_cells 保存。
header_note_cells 必须引用区块 header_rows 内的真实非空单元格。每个说明坐标都须以区块级 field=header_note_cells 的 evidence 逐格引用；移除已有说明坐标时也须引用被移除的原格。从已有列的 header_cells 移出说明时，同时提供剩余表头的证据，按原证据修正受影响的名称。即使原映射能解析，也应提出有依据的说明行修正。'''


def apply_suggestion(candidate, suggestion):
    if not isinstance(suggestion, dict):
        raise ValueError('AI 建议不是有效的映射对象，原数据未改变。')
    if 'mapping' in suggestion:
        proposed = suggestion['mapping']
        if proposed is None:
            return None, 0
        if not isinstance(proposed, dict):
            raise ValueError('AI 返回的完整映射格式无效。')
        proposed = deepcopy(proposed)
        if not isinstance(proposed.get('blocks'), list):
            raise ValueError('AI 返回的完整映射区块格式无效。')
        if proposed.get('sheet') != candidate.get('sheet') or proposed.get('fingerprint') != candidate.get('fingerprint'):
            raise ValueError('AI 建议改变了工作表或结构指纹，原数据未改变。')
        if len(proposed['blocks']) < len(candidate.get('blocks', [])):
            raise ValueError('AI 建议遗漏了已有区块，原数据未改变。')
        restored = 0
        if isinstance(proposed.get('blocks'), list) and len(proposed['blocks']) >= len(candidate.get('blocks', [])):
            for original, block in zip(candidate['blocks'], proposed['blocks']):
                if not isinstance(block, dict) or block.get('layout') != original.get('layout'):
                    continue
                columns = block.get('columns')
                if not isinstance(columns, list) or any(not isinstance(c, dict) for c in columns):
                    continue
                updates = {c.get('column'): c for c in columns}
                if len(updates) != len(columns):
                    raise ValueError('AI 建议重复使用了原表列，请核对后重试。')
                missing = [c for c in original['columns'] if c['column'] not in updates]
                if missing:
                    restored += len(missing)
                    block['columns'] = [{**c, **updates.pop(c['column'], {})} for c in original['columns']] + list(updates.values())
        return proposed, restored
    if not any(k in suggestion for k in ('column_updates', 'block_updates')):
        raise ValueError('AI 没有返回映射或字段修改建议，原数据未改变。')
    result = deepcopy(candidate)
    for field, allowed in (('block_updates', BLOCK_FIELDS), ('column_updates', COLUMN_FIELDS)):
        updates = suggestion.get(field, [])
        if not isinstance(updates, list):
            raise ValueError('AI 字段修改建议格式无效。')
        for update in updates:
            if not isinstance(update, dict):
                raise ValueError('AI 字段修改建议格式无效。')
            index = update.get('block_index')
            if type(index) is not int or not 0 <= index < len(result['blocks']):
                raise ValueError('AI 建议引用了不存在的区块。')
            target = result['blocks'][index]
            if field == 'column_updates':
                target = next((c for c in target['columns'] if c['column'] == update.get('column')), None)
                if target is None:
                    raise ValueError('AI 建议引用了不存在的原表列。')
            unknown = set(update) - allowed - {'block_index', 'column'}
            if unknown:
                raise ValueError('AI 建议包含不支持的字段修改。')
            target.update({k: v for k, v in update.items() if k in allowed})
    return result, 0
