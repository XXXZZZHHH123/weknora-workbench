"""Keep equal source labels as separate columns without inventing business meaning."""
from collections import defaultdict
import json


def distinguish_source_columns(mapping):
    groups = defaultdict(list)
    for block in mapping['blocks']:
        if block.get('layout') == 'matrix':
            continue
        for column in block['columns']:
            dimensions = column.get('dimensions', {})
            if '原表列' in dimensions and dimensions['原表列'] != column['column']:
                raise ValueError(f"{column['column']} 列的来源维度必须与真实原表列一致。")
            semantic = {k: v for k, v in dimensions.items() if k != '原表列'}
            key = (column['metric'], json.dumps(semantic, ensure_ascii=False, sort_keys=True),
                   block.get('name', ''), block.get('category_column', ''))
            groups[key].append(column)
    duplicates = []
    for columns in groups.values():
        if len(columns) > 1:
            for column in columns:
                column['dimensions'] = {**column.get('dimensions', {}), '原表列': column['column']}
            duplicates.append({'metric': columns[0]['metric'], 'columns': [c['column'] for c in columns]})
    return duplicates
