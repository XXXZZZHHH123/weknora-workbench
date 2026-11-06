"""Recall candidates from metadata, then retain explicit space-wide decisions.

No candidate is an identity decision. Only confirmed_default suppresses a copy.
The identifier registry describes vendor syntax, never indicator business names.
"""
from collections import defaultdict
from contextlib import contextmanager
from datetime import datetime, timezone
from decimal import Decimal, InvalidOperation, ROUND_HALF_UP, localcontext
import hashlib
import json
from pathlib import Path
import re
import unicodedata

from fastapi import HTTPException
from sqlalchemy import select, update
from sqlalchemy.exc import IntegrityError

from tin.config import settings
from tin.db import tenant_database_path
from tin.integration.workbench_scope import result_session_factory
from tin.models.indicator_dedup import IndicatorCorrespondence

POINTS = 500
STATES = {'pending', 'confirmed_default', 'keep_both', 'rejected'}
_FORMATS = json.loads(Path(__file__).with_name('indicator_code_formats.json').read_text())
CODE_FORMATS = [(item['vendor'], re.compile(r'(?<![A-Za-z0-9])(?:' + item['format'] + r')(?![A-Za-z0-9])', re.I))
                for item in _FORMATS]
# Explicit field metadata only: never IDs, filenames or answers.
METADATA_KEYS = ('name', 'metric', 'description', 'note', 'dimensions', 'headers', 'header',
                 'header_text', 'source_series_code', 'vendor_code', 'code', 'column_name', 'metadata')


def normalized(value):
    return ' '.join(unicodedata.normalize('NFKC', str(value or '')).split()).casefold()


def vendor_codes(field):
    raw = json.dumps({key: field[key] for key in METADATA_KEYS if key in field}, ensure_ascii=False)
    return {(vendor, match.group().upper()) for vendor, pattern in CODE_FORMATS for match in pattern.finditer(raw)}


def require_scope(scope):
    if scope is None or scope.scope_version != 2:
        raise HTTPException(409, '指标对应需要已核验的多来源研究台')
    if scope.restricted:
        raise HTTPException(403, '受限视角不开放指标对应确认与目录')
    return scope


def sources(scope):
    return {item['knowledge_base_id']: item for item in scope.sources if not item['delegated']}


def can_confirm(scope):
    # Equivalent to canManageKBPolicy for a central KB: local space admin +
    # valid direct KB permission. Source grants are freshly verified upstream.
    return bool(scope and scope.scope_version == 2 and not scope.restricted
                and scope.identity.role in {'owner', 'admin'}
                and any(item['source_type'] == 'central_dataset' and not item['delegated']
                        and item['tenant_id'] == scope.identity.tenant_id
                        and item.get('permission') in {'viewer', 'editor', 'admin'} for item in scope.sources))


@contextmanager
def correspondence_session(scope):
    require_scope(scope)
    # A separate space store is shared by every workbench, with no legacy facts.
    path = tenant_database_path(scope.identity.tenant_id).with_name('indicator-dedup.db')
    with result_session_factory(path)() as session:
        try:
            yield session
        except IntegrityError:
            session.rollback()
            raise HTTPException(409, '个人库指标对应发生并发冲突，请刷新后重新确认；未覆盖已有决定') from None


def pair_id(personal, default):
    identity = [[field['knowledge_base_tenant_id'], field['knowledge_base_id'], field['series_id']]
                for field in (personal, default)]
    return hashlib.sha256(json.dumps(identity, separators=(',', ':')).encode()).hexdigest()


def candidate_pairs(scope, fields):
    require_scope(scope)
    source_map = sources(scope)
    central, personal = [], []
    for field in fields:
        source = source_map.get(field.get('knowledge_base_id'))
        if source is None or field.get('knowledge_base_tenant_id') != source['tenant_id']:
            continue
        if source['source_type'] == 'central_dataset':
            central.append(field)
        elif source['source_type'] == 'knowledge_base':
            personal.append(field)
    by_code, by_weak = defaultdict(list), defaultdict(list)
    def weak(field):
        values = tuple(normalized(field.get(key)) for key in ('unit', 'frequency'))
        name = normalized(field.get('metric') or field.get('name'))
        return (name, *values) if name and all(values) else None
    for field in central:
        for code in vendor_codes(field):
            by_code[code].append(field)
        key = weak(field)
        if key:
            by_weak[key].append(field)
    found = {}
    for own in personal:
        matches = {}
        for code in vendor_codes(own):
            for default in by_code[code]:
                matches[default['series_id']] = default
        key = weak(own)
        if key:
            for default in by_weak[key]:
                matches[default['series_id']] = default
        for default in matches.values():
            shared = sorted(vendor_codes(own) & vendor_codes(default))
            identifier = pair_id(own, default)
            found[identifier] = {'id': identifier, 'personal': own, 'default': default,
                'candidate': {'strength': 'strong' if shared else 'weak',
                              'codes': [{'vendor': vendor, 'code': code} for vendor, code in shared],
                              'reason': '相同明确数据商编码' if shared else '完整名称、单位和频率规范化后相同'}}
            if len(found) > 10000:
                raise HTTPException(413, '可能重复的指标超过10000组，请缩小来源范围后核对')
    return list(found.values())


def _authorized(scope, row):
    available = sources(scope)
    own, default = available.get(row.personal_kb_id), available.get(row.default_kb_id)
    return bool(own and default and own['source_type'] == 'knowledge_base'
                and default['source_type'] == 'central_dataset'
                and own['tenant_id'] == row.personal_tenant_id
                and default['tenant_id'] == row.default_tenant_id)


def _current_pair(row, catalog):
    """Stored KB grants alone never authorize a withdrawn document's metadata."""
    own, default = catalog.get(row.personal_id), catalog.get(row.default_id)
    if not own or not default:
        return None
    if (own.get('knowledge_base_id') != row.personal_kb_id
            or own.get('knowledge_base_tenant_id') != row.personal_tenant_id
            or default.get('knowledge_base_id') != row.default_kb_id
            or default.get('knowledge_base_tenant_id') != row.default_tenant_id):
        return None
    return {'personal': own, 'default': default, 'candidate': row.candidate}


def raw_metadata(scope):
    """Read complete authorized metadata in bounded pages, never observations.

    This is correspondence recall, not a model-analysis catalog. Each source
    already limits its index to 25000 fields and a workbench has at most eight.
    """
    require_scope(scope)
    from tin.integration.page_facts import catalog_page
    catalog, datasets, offset, version, total = {}, [], 0, None, None
    while True:
        page = catalog_page(scope, offset=offset, limit=200, version=version, apply_dedup=False)
        if version is None:
            version, total, datasets = page['version'], page['total'], page.get('datasets', [])
            if total > 200000:
                raise HTTPException(413, '指标对应目录超过当前8个来源的元数据容量')
        if page['version'] != version or page['total'] != total:
            raise HTTPException(409, '指标目录在召回期间变化，请刷新后重试')
        for field in page['series']:
            if field['series_id'] in catalog:
                raise HTTPException(503, '指标对应目录分页出现重复')
            catalog[field['series_id']] = field
        following = page.get('next_offset')
        if following is None:
            if page.get('truncated') or len(catalog) != total:
                raise HTTPException(503, '指标对应目录未完整读取，未使用截断证据')
            return {'catalog': catalog, 'version': version, 'datasets': datasets}
        if not page['series'] or following != offset + len(page['series']):
            raise HTTPException(503, '指标对应目录分页进度无效')
        offset = following


def discover(scope, fields=None):
    require_scope(scope)
    if fields is None:
        fields = list(raw_metadata(scope)['catalog'].values())
    candidates = candidate_pairs(scope, fields)
    now = datetime.now(timezone.utc)
    with correspondence_session(scope) as session:
        existing = {row.id: row for row in session.scalars(select(IndicatorCorrespondence))}
        for item in candidates:
            if item['id'] in existing:
                continue
            own, default = item['personal'], item['default']
            session.add(IndicatorCorrespondence(id=item['id'], personal_id=own['series_id'],
                default_id=default['series_id'], personal_kb_id=own['knowledge_base_id'],
                default_kb_id=default['knowledge_base_id'], personal_tenant_id=own['knowledge_base_tenant_id'],
                default_tenant_id=default['knowledge_base_tenant_id'], personal=own, default=default,
                candidate=item['candidate'], state='pending', evidence=None, history=[], revision=1,
                created_at=now, updated_at=now))
        try:
            session.commit()
        except IntegrityError:
            # Concurrent metadata discovery can only add the same stable pair.
            session.rollback()
    return candidates


def batch_eligible(candidate, evidence):
    return bool(candidate.get('strength') == 'strong' and evidence
        and evidence.get('overlap_periods', 0) > 0 and evidence.get('mismatching_periods') == 0
        and evidence.get('matching_periods') == evidence.get('overlap_periods')
        and evidence.get('unit_match') is True and evidence.get('frequency_match') is True
        and evidence.get('precision_complete') is True)


def serialize(row, current=None):
    result = {key: getattr(row, key) for key in ('id', 'state', 'evidence', 'revision', 'history', 'confirmed_by', 'confirmed_at')}
    result.update(personal=current['personal'] if current else row.personal,
                  default=current['default'] if current else row.default,
                  candidate=current['candidate'] if current else row.candidate)
    result['batch_eligible'] = row.state == 'pending' and batch_eligible(result['candidate'], row.evidence)
    return result


def list_mappings(scope, *, discover_current=True):
    require_scope(scope)
    # Fresh document-level source inventory is authoritative on every read.
    catalog = raw_metadata(scope)['catalog']
    candidates = discover(scope, fields=list(catalog.values())) if discover_current else []
    current = {item['id']: item for item in candidates}
    with correspondence_session(scope) as session:
        rows = session.scalars(select(IndicatorCorrespondence).order_by(IndicatorCorrespondence.created_at, IndicatorCorrespondence.id)).all()
        items = []
        for row in rows:
            if not _authorized(scope, row):
                continue
            pair = _current_pair(row, catalog)
            if pair is None or row.id not in current and row.state == 'pending':
                continue
            items.append(serialize(row, current.get(row.id, pair)))
        return {'items': items, 'can_confirm': can_confirm(scope),
                'sources_revision': scope.sources_revision,
                'pending_count': sum(item['state'] == 'pending' for item in items)}


def confirmed_replacements(scope, identifiers=None):
    if scope is None or scope.scope_version != 2 or scope.restricted:
        return {}
    with correspondence_session(scope) as session:
        query = select(IndicatorCorrespondence).where(IndicatorCorrespondence.state == 'confirmed_default')
        if identifiers is not None:
            query = query.where(IndicatorCorrespondence.personal_id.in_(list(identifiers)))
        result = {}
        for row in session.scalars(query):
            if _authorized(scope, row):
                if row.personal_id in result and result[row.personal_id] != row.default_id:
                    raise HTTPException(409, '个人库指标存在冲突对应，请撤销后重新确认')
                result[row.personal_id] = row.default_id
        return result


def catalog_annotations(scope, fields, *, current_fields=None):
    if scope is None or scope.scope_version != 2 or scope.restricted:
        return fields
    if current_fields is None:
        current_fields = list(raw_metadata(scope)['catalog'].values())
    current_fields = list(current_fields)
    catalog = {field['series_id']: field for field in current_fields}
    candidates = discover(scope, fields=current_fields)
    current_ids = {item['id'] for item in candidates}
    identifiers = {field['series_id'] for field in fields}
    linked = defaultdict(list)
    with correspondence_session(scope) as session:
        rows = session.scalars(select(IndicatorCorrespondence).where(
            IndicatorCorrespondence.personal_id.in_(identifiers) | IndicatorCorrespondence.default_id.in_(identifiers))).all()
        for row in rows:
            if (not _authorized(scope, row) or _current_pair(row, catalog) is None
                    or row.state not in {'pending', 'confirmed_default'}):
                continue
            if row.state == 'pending' and row.id not in current_ids:
                continue
            value = {'id': row.id, 'personal_id': row.personal_id, 'default_id': row.default_id, 'state': row.state}
            linked[row.personal_id].append(value)
            linked[row.default_id].append(value)
    return [{**field, **({'dedup_status': 'confirmed_default' if any(pair['state'] == 'confirmed_default'
        for pair in linked[field['series_id']]) else 'pending', 'dedup_pairs': linked[field['series_id']]}
        if linked[field['series_id']] else {})} for field in fields]


annotate_catalog = catalog_annotations


def _decimal(row):
    raw = row.get('raw_value')
    value = Decimal(str(row['value']))
    precision = row.get('display_decimals')
    if type(precision) is int and 0 <= precision <= 15:
        return value, precision, True
    if raw is not None:
        try:
            original = Decimal(str(raw).replace(',', '').strip())
            if original.is_finite():
                return value, max(0, -original.as_tuple().exponent), True
        except InvalidOperation:
            pass
    return value, max(0, -value.as_tuple().exponent), False


def compare_series(personal, default, rows, *, version=None, source_versions=None):
    observations = defaultdict(dict)
    for row in rows:
        if row.get('series_id') not in {personal['series_id'], default['series_id']}:
            raise HTTPException(503, '对应核实返回了非目标指标')
        if row.get('value') is None:
            continue
        observations[row['series_id']][row['date']] = row
    own, central = observations[personal['series_id']], observations[default['series_id']]
    dates = sorted(set(own) & set(central))[-POINTS:]
    matching, mismatches, precise = 0, [], True
    for day in dates:
        value, decimals, known = _decimal(own[day])
        precise = precise and known
        quant = Decimal(1).scaleb(-decimals)
        try:
            other = Decimal(str(central[day]['value']))
            # Decimal's default 28 digits cannot quantize all finite floats.
            with localcontext() as context:
                context.prec = max(28, max(value.adjusted(), other.adjusted()) + decimals + 5)
                equal = value.quantize(quant, rounding=ROUND_HALF_UP) == other.quantize(quant, rounding=ROUND_HALF_UP)
        except InvalidOperation:
            raise HTTPException(503, '对应核实的数值精度无效') from None
        if equal:
            matching += 1
        else:
            mismatches.append(day)
    own_asof, default_asof = max(own, default=None), max(central, default=None)
    return {'matching_periods': matching, 'mismatching_periods': len(mismatches),
        'overlap_periods': len(dates), 'latest_mismatch_date': max(mismatches, default=None),
        'unit_match': bool(normalized(personal.get('unit'))) and normalized(personal.get('unit')) == normalized(default.get('unit')),
        'frequency_match': bool(normalized(personal.get('frequency'))) and normalized(personal.get('frequency')) == normalized(default.get('frequency')),
        'personal_unit': personal.get('unit'), 'default_unit': default.get('unit'),
        'personal_frequency': personal.get('frequency'), 'default_frequency': default.get('frequency'),
        'personal_as_of': own_asof, 'default_as_of': default_asof,
        'stale_default': bool(own_asof and default_asof and default_asof < own_asof),
        'precision_complete': precise and bool(dates), 'checked_points': POINTS, 'limited': True,
        'notice': '仅核实双方各自最近500期的重叠日期，按个人库原值显示的小数位四舍五入；不代表全部历史一致',
        'version': version, 'source_versions': source_versions or {},
        'checked_at': datetime.now(timezone.utc).isoformat()}


def _fresh_pair(scope, identifier):
    candidate = next((item for item in discover(scope) if item['id'] == identifier), None)
    if candidate is None:
        # Decisions remain traceable if source metadata no longer meets recall.
        with correspondence_session(scope) as session:
            row = session.get(IndicatorCorrespondence, identifier)
            if row is None or row.state == 'pending' or not _authorized(scope, row):
                raise HTTPException(409, '指标元数据或来源已变化，请重新召回候选')
            from tin.integration.page_facts import catalog_page
            page = catalog_page(scope, series_ids=[row.personal_id, row.default_id], apply_dedup=False)
            fields = {field['series_id']: field for field in page['series']}
            if row.personal_id not in fields or row.default_id not in fields:
                raise HTTPException(409, '对应序列已移除，未读取历史副本')
            candidate = {'id': identifier, 'personal': fields[row.personal_id],
                         'default': fields[row.default_id], 'candidate': row.candidate}
    return candidate


def _verify(scope, candidate):
    from tin.integration.page_facts import bounded_facts
    own, default = candidate['personal'], candidate['default']
    origins = {field['series_id']: {'knowledge_base_id': field['knowledge_base_id'],
                'tenant_id': field['knowledge_base_tenant_id']} for field in (own, default)}
    result = bounded_facts(scope, [own['series_id'], default['series_id']], 'indicators', points=POINTS,
        origins=origins, include_display_precision=True)
    if result.get('unavailable_series_ids'):
        raise HTTPException(409, '待核实指标已失效，请重新读取来源')
    fields = {field['series_id']: field for field in result['indicators']}
    for field in (own, default):
        actual = fields.get(field['series_id'])
        if not actual or any(actual.get(key) != field.get(key) for key in ('knowledge_base_id', 'knowledge_base_tenant_id')):
            raise HTTPException(409, '核实序列来源与候选证据不一致')
        if field.get('source_version') and actual.get('source_version') and field['source_version'] != actual['source_version']:
            raise HTTPException(409, '来源快照在核实期间变化，请重新读取')
    return compare_series(fields[own['series_id']], fields[default['series_id']], result['rows'],
                          version=result['version'], source_versions=result.get('source_versions'))


def verify_candidate(scope, identifier):
    require_scope(scope)
    candidate = _fresh_pair(scope, identifier)
    evidence = _verify(scope, candidate)
    with correspondence_session(scope) as session:
        row = session.get(IndicatorCorrespondence, identifier)
        revision = row.revision
        changed = session.execute(update(IndicatorCorrespondence).where(IndicatorCorrespondence.id == identifier,
            IndicatorCorrespondence.revision == revision).values(evidence=evidence,
            personal=candidate['personal'], default=candidate['default'], candidate=candidate['candidate'],
            revision=revision+1, updated_at=datetime.now(timezone.utc)))
        if changed.rowcount != 1:
            raise HTTPException(409, '指标对应记录已更新，请刷新')
        session.commit()
        session.refresh(row)
        return serialize(row, candidate)


def decide(scope, identifiers, revisions, state, *, batch=False):
    require_scope(scope)
    if not can_confirm(scope):
        raise HTTPException(403, '仅有中心来源读取权限的本空间管理员可确认指标对应；当前为只读')
    if state not in STATES - {'pending'} or not identifiers or len(identifiers) > 20 or len(set(identifiers)) != len(identifiers):
        raise HTTPException(422, '指标对应决定无效，每次最多20组')
    if len(identifiers) > 1 and not batch or batch and state != 'confirmed_default':
        raise HTTPException(422, '其余候选需逐条确认')
    current = {item['id']: item for item in discover(scope)}
    now = datetime.now(timezone.utc)
    with correspondence_session(scope) as session:
        rows = []
        for identifier in identifiers:
            row = session.get(IndicatorCorrespondence, identifier)
            if row is None or not _authorized(scope, row) or identifier not in current:
                raise HTTPException(404, '指标对应候选不存在或来源已移除')
            if row.default_tenant_id != scope.identity.tenant_id:
                raise HTTPException(403, '只能维护本空间中心来源的指标对应')
            if row.revision != revisions.get(identifier):
                raise HTTPException(409, '指标对应记录已更新，请刷新后确认')
            if row.state != 'pending':
                raise HTTPException(409, '已决定的对应请先撤销，再重新确认')
            rows.append(row)
        for row in rows:
            candidate = current[row.id]
            evidence = row.evidence
            if state == 'confirmed_default':
                if evidence is None:
                    raise HTTPException(422, '请先核实并查看重叠期证据，再确认以default库为准')
                fresh = _verify(scope, candidate)
                # The researcher must approve evidence they actually reviewed.
                visible = lambda value: {key: item for key, item in value.items() if key != 'checked_at'}
                if visible(fresh) != visible(evidence):
                    raise HTTPException(409, '核实证据或来源快照已变化，请重新核实并查看后确认')
                evidence = fresh
            if batch and not batch_eligible(candidate['candidate'], evidence):
                raise HTTPException(422, '批量确认仅适用于编码相同、重叠期全部一致、单位频率一致且原值精度明确的候选')
            if state == 'confirmed_default':
                conflicts = session.scalars(select(IndicatorCorrespondence).where(
                    IndicatorCorrespondence.personal_id == row.personal_id,
                    IndicatorCorrespondence.state == 'confirmed_default', IndicatorCorrespondence.id != row.id)).all()
                if conflicts:
                    raise HTTPException(409, '个人库指标已有其他default对应，请先撤销')
            history = [*row.history, {'action': 'decide', 'previous_state': row.state, 'state': state,
                'actor': scope.identity.user_id, 'at': now.isoformat(), 'evidence': evidence,
                'candidate': candidate['candidate'], 'personal': candidate['personal'], 'default': candidate['default']}]
            result = session.execute(update(IndicatorCorrespondence).where(IndicatorCorrespondence.id == row.id,
                IndicatorCorrespondence.revision == revisions[row.id]).values(state=state, evidence=evidence,
                personal=candidate['personal'], default=candidate['default'], candidate=candidate['candidate'],
                history=history, confirmed_by=scope.identity.user_id, confirmed_at=now,
                revision=row.revision+1, updated_at=now))
            if result.rowcount != 1:
                raise HTTPException(409, '指标对应记录已更新，请刷新后确认')
        session.commit()
        return {'items': [serialize(session.get(IndicatorCorrespondence, row.id), current[row.id]) for row in rows]}


def undo(scope, identifier, revision):
    require_scope(scope)
    if not can_confirm(scope):
        raise HTTPException(403, '仅有中心来源读取权限的本空间管理员可撤销指标对应')
    catalog = raw_metadata(scope)['catalog']
    with correspondence_session(scope) as session:
        row = session.get(IndicatorCorrespondence, identifier)
        pair = _current_pair(row, catalog) if row is not None else None
        if row is None or not _authorized(scope, row) or pair is None:
            raise HTTPException(404, '指标对应不存在或当前来源字段无读取授权')
        if row.default_tenant_id != scope.identity.tenant_id:
            raise HTTPException(403, '只能维护本空间中心来源的指标对应')
        if row.revision != revision or row.state == 'pending':
            raise HTTPException(409, '指标对应已更新或已撤销，请刷新')
        now = datetime.now(timezone.utc)
        history = [*row.history, {'action': 'undo', 'previous_state': row.state, 'state': 'pending',
            'actor': scope.identity.user_id, 'at': now.isoformat(), 'evidence': row.evidence,
            'confirmation_actor': row.confirmed_by, 'confirmation_at': row.confirmed_at.isoformat() if row.confirmed_at else None}]
        changed = session.execute(update(IndicatorCorrespondence).where(IndicatorCorrespondence.id == identifier,
            IndicatorCorrespondence.revision == revision).values(state='pending', history=history,
            confirmed_by=None, confirmed_at=None, revision=revision+1, updated_at=now))
        if changed.rowcount != 1:
            raise HTTPException(409, '指标对应记录已更新，请刷新')
        session.commit()
        session.refresh(row)
        return serialize(row, pair)
