"""Display metadata pages without opening an observation projection."""
from tin.integration.kb_facts import catalog_page


def indicator_row(spec):
    macro = spec.get('source_kind') == 'macro'
    metadata = spec.get('field_metadata') or {}
    header_notes = metadata.get('header_notes')
    role = metadata.get('role')
    labels = {'numeric': '数值指标', 'text_dimension': '文本维度', 'annotation': '说明列'}
    quality = '; '.join(part for part in [labels.get(role),
                                           '单位为人工确认，未在原表找到对应文字' if metadata.get('unit_source') == 'declared' else None,
                                           '单位待核对' if not spec.get('unit') and role not in ('text_dimension', 'annotation') else None,
                                           '频率未注明' if not spec.get('frequency') else None] if part)
    return {'series_id': spec['series_id'], 'name': spec.get('metric') or spec.get('name') or spec['series_id'],
            'category': '宏观数据' if macro else '知识库字段', 'macro': macro,
            'caliber': spec.get('dimensions') if isinstance(spec.get('dimensions'), dict) else {},
            'unit': spec.get('unit') or '—', 'source': spec.get('source_file') or '知识库结构化', 'sheet': spec.get('sheet') or '',
            'frequency': spec.get('frequency') or '未注明', 'fetch_mode': 'readonly', 'status': '可用',
            'registered': True, 'vendor_code': None, 'is_proxy': False, 'note': quality, 'field_metadata': spec.get('field_metadata'),
            **({'header_notes': header_notes} if header_notes else {}),
            'knowledge_base_id': spec.get('knowledge_base_id'), 'source_kind': spec.get('source_kind'),
            'dedup_status': spec.get('dedup_status'), 'dedup_pairs': spec.get('dedup_pairs', [])}


def indicator_page(scope, *, offset=0, limit=200, search='', kind='all', identifiers=None):
    page = catalog_page(scope, offset=offset, limit=limit, search=search,
                        series_ids=identifiers, source_kind=None if kind == 'all' else kind)
    rows = [indicator_row(spec) for spec in page['series']]
    return {**page, 'indicators': rows,
            'groups': [{'name': name, 'rows': [row for row in rows if row['category'] == name]}
                       for name in dict.fromkeys(row['category'] for row in rows)]}
