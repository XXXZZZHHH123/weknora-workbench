"""Complete observation windows within the structured judgment protocol."""
from datetime import date, timedelta

from fastapi import HTTPException


def paged_observations(scope, identifiers, day, limit, *, direct_only=False,
                       versions=None, version_notice='均线来源在分页期间变化，请刷新',
                       include_invalid=True, upstream_error_detail=False):
    """Page backward by each series' real dates, retaining invalid slots by default.

    ``versions`` may be shared across window groups in one read. Catalog and
    facts envelopes vary with the query; only binding snapshot versions can be
    compared across pages. Direct reads also fence facts against the catalog.
    Callers bound the complete window; each wire request has its own budget.
    """
    from tin.integration.page_facts import bounded_facts
    identifiers = list(dict.fromkeys(identifiers))
    rows = {sid: [] for sid in identifiers}
    pending = {sid: day for sid in identifiers}
    versions = {} if versions is None else versions
    while pending:
        groups = {}
        for sid, boundary in pending.items():
            size = min(121, limit - len(rows[sid]))
            groups.setdefault((boundary, size), []).append(sid)
        following = {}
        for (boundary, size), ids in groups.items():
            batch_size = min(256, 20000 // size)
            for offset in range(0, len(ids), batch_size):
                selected = ids[offset:offset + batch_size]
                options = {'direct_only': True} if direct_only else {}
                if include_invalid:
                    options['include_invalid'] = True
                if upstream_error_detail:
                    options['upstream_error_detail'] = True
                facts = bounded_facts(scope, selected, 'judgments', day=boundary.isoformat(),
                                      points=size, **options)
                for binding, version in facts.get('source_versions', {}).items():
                    if binding in versions and versions[binding] != version:
                        raise HTTPException(409, version_notice)
                    versions[binding] = version
                for sid in selected:
                    page = sorted([point for point in facts['rows'] if point.get('series_id') == sid],
                                  key=lambda point: point['date'])
                    # bounded_facts validates IDs, unique dates, statuses and
                    # cutoff. Missing observations keep their actual position.
                    rows[sid].extend(page)
                    if len(page) == size and len(rows[sid]) < limit:
                        earliest = date.fromisoformat(page[0]['date'])
                        if earliest > date.min:
                            following[sid] = earliest - timedelta(days=1)
        pending = following
    return {sid: sorted(points, key=lambda point: point['date']) for sid, points in rows.items()}
