"""Native chart definitions from the research facts' currently approved snapshots."""
from contextlib import ExitStack
from dataclasses import replace
from xml.etree.ElementTree import ParseError
from zipfile import BadZipFile

from fastapi import HTTPException

from . import snapshot_metadata
from .native_charts import SUPPORTED_CHART_GROUPS
from .research_catalog import current_index
from .research_facts import _hash, research_series_id
from .research_facts_v2 import _originals
from .workspaces import current_identity


def parameters(body):
    """The chart read accepts only bounded pagination, never caller-supplied sources."""
    if not isinstance(body, dict) or set(body) - {'offset', 'limit'}:
        raise ValueError('原表图表读取只接受 offset 和 limit')
    offset, limit = body.get('offset', 0), body.get('limit', 200)
    if type(offset) is not int or offset < 0 or type(limit) is not int or not 1 <= limit <= 200:
        raise ValueError('原表图表分页参数无效')
    return offset, limit


def _curve(curve, fields, confirmed, knowledge_base_id, knowledge_id):
    ref = curve.get('reference') or {}
    field = fields.get(curve.get('source_series_id'))
    if field is not None and (field.get('sheet') != ref.get('sheet') or field.get('sheet') not in confirmed):
        field = None
    status, reason = curve['status'], curve['reason']
    if status == 'needs_review':
        # The public protocol has no ambiguity status: retain the explanation
        # while requiring a source mapping instead of choosing one field.
        status = 'pending_source'
    if ref and ref['sheet'] not in confirmed:
        status, reason = 'unconfirmed_sheet', '曲线源工作表尚未确认，未提供研究指标身份。'
    elif curve['chart_type'] not in SUPPORTED_CHART_GROUPS:
        status, reason, field = 'unsupported', '暂不支持此原生图表类型。', None
    elif status in {'linked', 'no_data'} and field is None:
        status, reason = 'pending_source', '曲线源字段不在当前已确认目录中，请重新核对源表映射。'
    style = curve.get('style') or {}
    return {
        'label': curve['label'], 'chart_type': curve['chart_type'], 'axis': curve['axis'],
        'series_id': research_series_id(knowledge_base_id, knowledge_id, field['series_id']) if field else None,
        'status': status, 'reason': reason,
        'first_date': curve.get('first_date') if field else None,
        'last_date': curve.get('last_date') if field else None,
        'unit': field.get('unit') if field else None,
        'descriptor': {'sheet': ref.get('sheet', ''), 'metric': field.get('metric', '') if field else '',
                       'column': ref.get('column', ''), 'start_row': ref.get('start_row'), 'end_row': ref.get('end_row')},
        'style': {key: style.get(key) for key in ('color', 'width_pt', 'dash')},
    }


def _chart(chart, knowledge_base_id, info, fields):
    knowledge_id = info['source_knowledge_id']
    confirmed = set(info['confirmed_sheets'])
    return {
        'chart_key': 'nc-' + _hash([knowledge_base_id, knowledge_id, chart['chart_id']])[:32],
        'knowledge_id': knowledge_id, 'source_file': info['source_file'],
        'sheet': chart['sheet'], 'title': chart['title'],
        'axes': [{key: axis.get(key) for key in ('id', 'type', 'title', 'position')}
                 for axis in chart['axes'] if axis['type'] in {'dateAx', 'catAx', 'valAx'}],
        'curves': [_curve(curve, fields, confirmed, knowledge_base_id, knowledge_id) for curve in chart['curves']],
    }


def read_current(registry, body, sources):
    """Lease the same verified catalogs as multi-source facts through the full read."""
    offset, limit = parameters(body)
    identity = current_identity.get()
    if identity is None or not identity.workbench_id or sources is None:
        raise HTTPException(403, '原表图表读取缺少已核验的研究台来源')
    if identity.scope_version != 2 or identity.sources_revision < 1:
        raise HTTPException(409, '图表库只支持新版来源授权，请重新打开研究台')
    if any(source['delegated'] for source in sources):
        raise HTTPException(403, '受限视角暂不支持图表库读取')
    charts, total, seen = [], 0, set()
    with ExitStack() as leases:
        for source in sorted(sources, key=lambda item: item['id']):
            local = replace(identity, knowledge_base_id=source['knowledge_base_id'],
                            knowledge_base_tenant_id=source['tenant_id'])
            marker = current_identity.set(local)
            try:
                objects = registry.objects()
                store, manager = objects['store'], objects['manager']
                _, metadata = leases.enter_context(current_index(store, manager, _originals(source)))
                for info in sorted(metadata['datasets'], key=lambda item: item['source_knowledge_id']):
                    try:
                        fields = {field['series_id']: field for field in
                                  snapshot_metadata.document(store._path(info['dataset_id']) / 'series.json')}
                        index = store.chart_index(info['dataset_id'])
                    except (OSError, ValueError, KeyError, TypeError, ParseError, BadZipFile):
                        raise HTTPException(409, '原生图表索引或来源无法核验，请重新核对') from None
                    for chart in sorted(index['charts'], key=lambda item: item['chart_id']):
                        key = (source['knowledge_base_id'], info['source_knowledge_id'], chart['chart_id'])
                        if key in seen:
                            continue
                        seen.add(key)
                        if offset <= total < offset + limit:
                            charts.append(_chart(chart, source['knowledge_base_id'], info, fields))
                        total += 1
            finally:
                current_identity.reset(marker)
    return {'charts': charts, 'total': total, 'offset': offset,
            'next_offset': offset + len(charts) if offset + len(charts) < total else None}
