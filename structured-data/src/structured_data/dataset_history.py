"""Explain changes in queryable coverage, independently of snapshot identifiers."""
def describe_history(items, *, source_keys=None):
    previous = {}
    for item in sorted(items, key=lambda d: d['imported_at']):
        keys = source_keys.get(item['dataset_id'], (('filename', item['source_file']),)) if source_keys is not None else (('filename', item['source_file']),)
        candidates = [previous[key] for key in keys if key in previous]
        prior = max(candidates, key=lambda row: row['imported_at']) if candidates else None
        names = {s['name'] for s in item['sheets']}
        if prior is None:
            event = {'kind': 'first_parse', 'summary': f'首次解析，可查询 {len(names)} 张工作表', 'sheets': []}
        elif item['sha256'] != prior['sha256']:
            event = {'kind': 'source_update', 'summary': '原文件内容更新，重新导入', 'sheets': []}
        else:
            added = sorted(names - {s['name'] for s in prior['sheets']})
            changed = item.get('changed_sheets') or []
            if added:
                event = {'kind': 'expand_scope', 'summary': '新增可查询工作表：' + '、'.join(added), 'sheets': added}
            elif changed:
                event = {'kind': 'mapping_update', 'summary': '更新工作表映射：' + '、'.join(changed), 'sheets': changed}
            else:
                event = {'kind': 'mapping_update', 'summary': '更新解析规则或字段，工作表范围未增加', 'sheets': []}
        item['import_event'] = event
        for key in keys:
            previous[key] = item
    return items
