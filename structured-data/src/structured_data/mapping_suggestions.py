"""Apply compact AI edits while preserving columns omitted from the response."""
from copy import deepcopy


BLOCK_FIELDS = {'name', 'date_mode', 'date_column', 'date_cell', 'date_format',
                'category_column', 'category_name', 'row_label_column',
                'header_rows', 'data_start_row', 'data_end_row', 'row_rules'}
COLUMN_FIELDS = {'metric', 'unit', 'dimensions', 'period', 'header_cells',
                 'frequency', 'frequency_cells'}


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
