"""Find withdrawn fields from the verified publication lineage of a current original."""
from collections import defaultdict
from contextlib import ExitStack
from dataclasses import replace
from itertools import groupby, zip_longest
from math import isfinite

from fastapi import HTTPException

from . import snapshot_metadata
from .research_catalog import current_index, page
from .research_facts import research_series_id
from .research_facts_v2 import _originals
from .workspaces import current_identity


def _positions(connection, fields, manifest):
    mappings = {sheet['name']: sheet.get('mapping') or {} for sheet in manifest['sheets']}
    by_id = {field['series_id']: field for field in fields}
    if not by_id:
        return {}
    positions = defaultdict(list)
    # Derive the physical column from observations so historical catalogues
    # with merged or multi-column header evidence still have a real location.
    for sid, first, last in connection.execute("""
        SELECT series_id, min(regexp_extract(source_cell, '^([A-Z]+)[0-9]+$', 1)),
                          max(regexp_extract(source_cell, '^([A-Z]+)[0-9]+$', 1))
        FROM observations WHERE series_id IN (SELECT unnest(?)) GROUP BY series_id
        """, [list(by_id)]).fetchall():
        field = by_id[sid]
        sheet, block = field.get('sheet'), field.get('block_index')
        blocks = mappings.get(sheet, {}).get('blocks', [])
        if not first or first != last or type(block) is not int or not 0 <= block < len(blocks):
            continue
        rule = blocks[block]
        if rule.get('layout') == 'matrix' or first not in {c['column'] for c in rule['columns']}:
            continue
        category = None
        if rule.get('category_column'):
            category = field.get('dimensions', {}).get(rule.get('category_name', '类别'))
            if not isinstance(category, str) or not category:
                continue
        positions[sheet, block, first, category].append(field)
    return positions


def _observations(connection, identifiers):
    result = connection.execute("""
        SELECT array_index, date, source_cell, period, value, value_status, raw_value
        FROM observations
        JOIN (SELECT unnest(?) AS sid, generate_subscripts(?, 1) AS array_index) AS target
          ON series_id = sid
        ORDER BY array_index, date, source_cell, period, value, value_status, raw_value
        """, [identifiers, identifiers])

    def batches():
        while rows := result.fetchmany(2048):
            yield from rows

    return groupby(batches(), key=lambda row: row[0])


def _equal_points(old_connection, new_connection, pairs):
    if not pairs:
        return
    old = _observations(old_connection, [item[1]['series_id'] for item in pairs])
    new = _observations(new_connection, [item[2]['source_series_id'] for item in pairs])
    for (i, old_rows), (j, new_rows) in zip(old, new, strict=True):
        if i != j:
            raise ValueError('观测对应位置变化')
        count = 0
        latest = None
        equal = True
        for before, after in zip_longest(old_rows, new_rows):
            if before != after or before is None:
                equal = False
                continue
            _, observed_date, _, _, value, status, raw_value = before
            if value is not None:
                if not isfinite(value):
                    equal = False
                    continue
            elif status not in ('text_dimension', 'annotation') or raw_value is None:
                continue
            count += 1
            if observed_date is not None:
                latest = observed_date
        if equal and count:
            position, old_field, new_field = pairs[i - 1]
            yield position, old_field, new_field, count, latest.isoformat() if latest else None


def _snapshot(store, dataset, info, leased):
    folder = store._path(dataset)
    for name in ('manifest.json', 'series.json', 'source.xlsx'):
        leased.append(snapshot_metadata.identity(folder / name))
    manifest = snapshot_metadata.document(folder / 'manifest.json')
    if (manifest.get('dataset_id') != dataset or manifest.get('import_id') != info['import_id']
            or manifest.get('sha256') != info['sha256']
            or snapshot_metadata.hashes(folder / 'source.xlsx')[1] != info['sha256']):
        raise ValueError('历史快照不属于当前核对记录或原件')
    fields = snapshot_metadata.document(folder / 'series.json')
    return manifest, fields


def _remaps(store, info, current, source, version):
    leased = []
    dataset = info['dataset_id']
    manifest, _ = _snapshot(store, dataset, info, leased)
    if not manifest.get('parent_dataset_id'):
        return []
    source_keys = {'knowledge_base_id': source['knowledge_base_id'],
                   'knowledge_base_tenant_id': source['tenant_id'],
                   'source_binding_id': source['id'], 'source_knowledge_id': info['source_knowledge_id'],
                   'source_sha256': info['sha256']}
    active = {field['source_series_id']: field for field in current}
    seen_fields = set(active)
    seen_datasets = {dataset}
    results = []
    current_manifest = manifest
    with ExitStack() as observations:
        new_connection = None
        while parent := manifest.get('parent_dataset_id'):
            if parent in seen_datasets:
                raise ValueError('历史快照关系无法完整核验')
            seen_datasets.add(parent)
            manifest, fields = _snapshot(store, parent, info, leased)
            published = {sheet['name'] for sheet in manifest.get('coverage', [])
                         if sheet.get('status') == 'confirmed'}
            withdrawn = [field for field in fields if field['sheet'] in published
                         and field['series_id'] not in seen_fields]
            seen_fields.update(field['series_id'] for field in withdrawn)
            if not withdrawn:
                continue
            if new_connection is None:
                new_connection = observations.enter_context(store._connect(dataset))
                leased.append(snapshot_metadata.identity(store._path(dataset) / 'data.duckdb'))
                new_positions = _positions(new_connection,
                    [{**field, 'series_id': sid} for sid, field in active.items()], current_manifest)
            with store._connect(parent) as old_connection:
                leased.append(snapshot_metadata.identity(store._path(parent) / 'data.duckdb'))
                old_positions = _positions(old_connection, withdrawn, manifest)
                pairs = []
                for position, old_fields in old_positions.items():
                    new_fields = new_positions.get(position, [])
                    if len(old_fields) == len(new_fields) == 1:
                        pairs.append((position, old_fields[0], active[new_fields[0]['series_id']]))
                for position, old, target, points, latest in _equal_points(old_connection, new_connection, pairs):
                    sid = research_series_id(source['knowledge_base_id'], info['source_knowledge_id'], old['series_id'])
                    results.append({'old': {**source_keys, 'series_id': sid,
                        'dataset_id': parent, 'source_file': info['source_file'],
                        **{key: old.get(key) for key in ('metric', 'unit', 'frequency')}},
                        'new_series_id': target['series_id'], 'new_dataset_id': dataset, 'source_version': version,
                        'sheet': position[0], 'block_index': position[1], 'column': position[2], 'category': position[3],
                        'matching_periods': points, 'latest_date': latest})
    if any(snapshot_metadata.identity(item[0]) != item for item in leased):
        raise ValueError('历史快照在核实期间变化')
    return results


def read_current(registry, body, sources):
    if not isinstance(body, dict) or body:
        raise ValueError('重映射候选读取只接受空对象')
    identity = current_identity.get()
    if identity is None or not identity.workbench_id or identity.scope_version != 2 or sources is None:
        raise HTTPException(403, '重映射候选缺少已核验的研究台范围')
    if any(source['delegated'] for source in sources):
        raise HTTPException(403, '受限视角不开放重映射候选')
    items = []
    try:
        with ExitStack() as leases:
            for source in sources:
                if source['tenant_id'] != identity.tenant_id:
                    continue
                marker = current_identity.set(replace(identity, knowledge_base_id=source['knowledge_base_id'],
                                                      knowledge_base_tenant_id=source['tenant_id']))
                try:
                    objects = registry.objects()
                    store, manager = objects['store'], objects['manager']
                    connection, metadata = leases.enter_context(current_index(store, manager, _originals(source)))
                    if not metadata['datasets']:
                        continue
                    fields, _, _ = page(connection, limit=25000)
                    by_original = defaultdict(list)
                    for field in fields:
                        by_original[field['source_knowledge_id']].append(field)
                    for info in metadata['datasets']:
                        items.extend(_remaps(store, info, by_original[info['source_knowledge_id']],
                                             source, metadata['version']))
                        if len(items) > 10000:
                            raise HTTPException(413, '重映射候选超过10000项，请缩小来源范围')
                finally:
                    current_identity.reset(marker)
    except (OSError, ValueError, KeyError, TypeError):
        raise HTTPException(409, '无法完整核验重映射的原件、版本或观测，请重新读取') from None
    return {'items': items}
