"""A 12-card refresh must seek cached series, not download or materialize a whole KB again."""
from datetime import date, timedelta
import hashlib
import json

import httpx
from sqlalchemy import event
from sqlalchemy.engine import Engine

from tests.test_knowledge_facts import canonical, FIRST, api, page
from tests.test_knowledge_fact_cache import close_projection_connections


def test_twelve_cards_reuse_large_verified_cache_and_indexed_bounded_reads(canonical, monkeypatch):
    client, headers, state, _ = canonical
    original = httpx.request
    identifiers = ['kb-' + format(index + 1, '048x') for index in range(377)]
    specs = [{'series_id': identifier, 'source_knowledge_id': 'source-one', 'dataset_id': 'd' * 24,
        'source_series_id': 'large-column-' + str(index), 'metric': 'Synthetic field ' + str(index),
        'unit': '件', 'frequency': '日', 'sheet': 'Synthetic', 'date_mode': 'date',
        'source_sha256': 'e' * 64, 'source_file': 'synthetic-large.xlsx'} for index, identifier in enumerate(identifiers)]
    version = hashlib.sha256(b'isolated-market-budget-377-fields-80-days').hexdigest()
    first = date(2026, 1, 2) - timedelta(days=79)
    observations = [{'series_id': identifier, 'source_series_id': specs[index]['source_series_id'],
        'source_knowledge_id': 'source-one', 'dataset_id': 'd' * 24,
        'date': (first + timedelta(days=day)).isoformat(), 'value': index * 1000 + day,
        'value_status': 'valid', 'source_cell': 'B' + str(index * 80 + day + 2),
        'date_cell': 'A' + str(index * 80 + day + 2), 'raw_value': index * 1000 + day}
        for index, identifier in enumerate(identifiers) for day in range(80)]
    transfers, metadata_checks = [], []

    def large_facts(method, url, **kwargs):
        response = original(method, url, **kwargs)
        if str(url).endswith('/api/research/facts'):
            body = json.loads(kwargs['content'])
            document = response.json()
            document['version'] = version
            if body.get('metadata_only'):
                metadata_checks.append(True)
            else:
                offset, limit = body.get('offset', 0), body.get('limit', 10000)
                rows = observations[offset:offset + limit]
                next_offset = offset + len(rows)
                document.update(indicators=specs, rows=rows, total=len(observations), offset=offset,
                    next_offset=next_offset if next_offset < len(observations) else None,
                    truncated=next_offset < len(observations))
                transfers.append(len(rows))
            return httpx.Response(200, json=document, request=response.request)
        return response

    monkeypatch.setattr(httpx, 'request', large_facts)
    warm = client.get(api(FIRST), headers=headers)
    assert warm.status_code == 200, warm.text
    assert warm.json()['total'] == 377
    assert sum(transfers) == 377 * 80
    warmed_pages, checked_before = len(transfers), len(metadata_checks)
    reads, plans, metadata_reads = [], [], []

    def measured(connection, cursor, statement, parameters, context, executemany):
        normalized = ' '.join(statement.lower().replace('"', '').split())
        if normalized.startswith('select indicators.') and 'from indicators' in normalized:
            metadata_reads.append(normalized)
        if normalized.startswith('select') and 'from observations' in normalized:
            reads.append((normalized, parameters))
            if 'observations.series_id = ?' in normalized:
                plan = connection.exec_driver_sql('EXPLAIN QUERY PLAN ' + statement, parameters).fetchall()
                plans.append(' '.join(str(row[-1]) for row in plan))

    event.listen(Engine, 'before_cursor_execute', measured)
    try:
        for with_date in (True, False):
            parameters = [('series_id', identifier) for identifier in identifiers[:12]]
            if with_date:
                parameters.append(('date', '2026-01-02'))
            response = client.get(page(FIRST, '/api/sn/market/core'),
                params=parameters, headers=headers)
            assert response.status_code == 200, response.text
            assert response.json()['series_ids'] == identifiers[:12]
            assert response.json()['ready_count'] == 12
            close_projection_connections()
    finally:
        event.remove(Engine, 'before_cursor_execute', measured)
    assert len(transfers) == warmed_pages, 'Warm card reads must not transfer any whole-source observation page'
    assert len(metadata_checks) > checked_before, 'Reuse must still freshly verify current source/version'
    assert len(metadata_reads) == 2
    assert all('indicators.series_id in (' in statement for statement in metadata_reads)
    selected_reads = [statement for statement, _ in reads if 'observations.series_id = ?' in statement]
    assert len(selected_reads) == 2 * 12 * 2
    assert all('limit' in statement for statement, _ in reads)
    assert len(reads) == 2 * (12 * 2 + 1), 'Only latest/previous per selected field and one indexed-boundary/cutoff query are needed'
    assert len(plans) == len(selected_reads)
    assert all('_kb_obs_lookup' in plan and 'SEARCH' in plan for plan in plans), plans
