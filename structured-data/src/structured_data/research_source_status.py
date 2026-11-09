"""Current original review metadata, including reviews without a published dataset."""
from contextlib import ExitStack, contextmanager
from dataclasses import replace

from fastapi import HTTPException

from . import snapshot_metadata
from .imports import sheet_field_count
from .research_facts import current_source_records
from .research_facts_v2 import _originals
from .workspaces import current_identity


SUMMARY_KEYS = ('confirmed', 'pending', 'pending_fields', 'pending_without_fields')


def _summary(sheets, review_sheets):
    # Empty, drawing-only and verified no-data (automatically skipped) sheets
    # have no data mapping to confirm; the knowledge-base review page does not
    # list them as pending either. Preserve their raw status in the response.
    pending = [item for item, raw in zip(sheets, review_sheets)
               if item['status'] not in {'confirmed', 'empty'}
               and not raw.get('automatic_skip')
               and not (raw.get('layout') == 'chart_or_image'
                        and not (raw.get('mapping') or {}).get('blocks')
                        and (raw.get('rows') or 0) <= 2)]
    return {'confirmed': sum(item['status'] == 'confirmed' for item in sheets),
            'pending': len(pending), 'pending_fields': sum(item['field_count'] for item in pending),
            # Pending sheets whose structure was not recognized expose no field
            # to confirm; callers must not describe them as fields awaiting review.
            'pending_without_fields': sum(not item['field_count'] for item in pending)}


def _review_files(manager):
    return [snapshot_metadata.identity(path) for path in sorted(manager.directory.glob('*/review.json'))]


@contextmanager
def _current_reviews(store, manager, originals):
    """Lease review and original identities just as the chart catalog leases snapshots."""
    try:
        with manager.lock:
            before = _review_files(manager)
            files, results = [], []

            def document(path):
                files.append(snapshot_metadata.identity(path))
                return snapshot_metadata.document(path)

            for original, record in current_source_records(manager, originals, include_unpublished=True):
                review = document(manager.path(record['import_id']) / 'review.json')
                if review.get('import_id') != record['import_id']:
                    raise ValueError('核对记录身份不符')
                catalog = []
                if record.get('dataset_id'):
                    folder = store._path(record['dataset_id'])
                    manifest = document(folder / 'manifest.json')
                    source_path = folder / 'source.xlsx'
                    catalog = document(folder / 'series.json')
                    source_file = manifest['source_file']
                    expected_sha = manifest['sha256']
                else:
                    source_path = manager.source_path(review)
                    source_file = manager.display_source_file(review)
                    expected_sha = review['sha256']
                files.append(snapshot_metadata.identity(source_path))
                md5, sha = snapshot_metadata.hashes(source_path)
                if original['file_hash'] not in {md5, sha} or expected_sha != sha:
                    raise ValueError('当前原件哈希不符')
                review_sheets = review['sheets']
                sheets = [{'name': sheet['name'], 'status': sheet['status'],
                           'field_count': sheet_field_count(sheet) if sheet.get('mapping') else
                           sum(field.get('sheet') == sheet['name'] for field in catalog)}
                          for sheet in review_sheets]
                results.append({'knowledge_id': original['knowledge_id'], 'source_file': source_file,
                                'sheets': sheets, 'summary': _summary(sheets, review_sheets)})
        yield results
        with manager.lock:
            if (_review_files(manager) != before
                    or any(snapshot_metadata.identity(item[0]) != item for item in files)):
                raise ValueError('核对记录或原件在读取期间变化')
    except (OSError, ValueError, KeyError, TypeError, AttributeError):
        raise HTTPException(409, '当前工作表核对记录或原件无法稳定核验，请重新核对') from None


def read_current(registry, body, sources):
    if not isinstance(body, dict) or body:
        raise ValueError('来源状态读取只接受空对象，不接受调用方指定来源')
    identity = current_identity.get()
    if identity is None or not identity.workbench_id or sources is None:
        raise HTTPException(403, '来源状态读取缺少已核验的研究台来源')
    if identity.scope_version != 2 or identity.sources_revision < 1:
        raise HTTPException(409, '来源状态只支持新版来源授权，请重新打开研究台')
    if any(source['delegated'] for source in sources):
        raise HTTPException(403, '受限视角暂不支持来源状态读取')
    results, seen = [], set()
    with ExitStack() as leases:
        for source in sorted(sources, key=lambda item: item['id']):
            local = replace(identity, knowledge_base_id=source['knowledge_base_id'],
                            knowledge_base_tenant_id=source['tenant_id'])
            marker = current_identity.set(local)
            try:
                objects = registry.objects()
                rows = leases.enter_context(_current_reviews(objects['store'], objects['manager'], _originals(source)))
                for row in rows:
                    key = (source['knowledge_base_id'], row['knowledge_id'])
                    if key not in seen:
                        seen.add(key)
                        results.append(row)
            finally:
                current_identity.reset(marker)
    return {'sources': results, 'summary': {
        key: sum(row['summary'][key] for row in results) for key in SUMMARY_KEYS}}
