"""Recover a verified original's own historical review into its knowledge base.

Callers must authenticate write access to the native original, and verify the
actual downloaded bytes before calling recover(). Never enumerate this data in
knowledge-base GETs, match filenames, or search another tenant's directory.
"""
from copy import deepcopy
from datetime import datetime, timezone
import hashlib
import os
from pathlib import Path
import shutil
import tempfile

from .imports import atomic_json

HUMAN_REVIEWED = {'confirmed', 'template_ready'}
RECOVERED_REVIEW_FIELDS = (
    'rule_version', 'source_file', 'sha256', 'use_reviewed_mappings',
    'sheets', 'sheet_count', 'scope', 'sample_rows', 'dataset_id',
    'publish_status', 'chart_catalog_preparation',
)


def candidate(legacy, source, current=None):
    if current and (current.get('legacy_recovery') or
                    any(s.get('status') in HUMAN_REVIEWED for s in current.get('sheets', []))):
        return None
    matches = []
    for row in legacy.list(include_samples=False):
        doc = legacy.get(row['import_id'])
        if not doc.get('dataset_id') or not any(s.get('status') in HUMAN_REVIEWED for s in doc['sheets']):
            continue
        path = legacy.source_path(doc)
        if path.is_file() and path.stat().st_size == source['file_size']:
            with path.open('rb') as stream:
                if hashlib.file_digest(stream, 'md5').hexdigest() == source['file_hash']:
                    matches.append(doc)
    if len(matches) > 1:
        raise ValueError('原空间有多份不同的已核对记录，请先选择要恢复的历史记录；不会重新扫描或覆盖。')
    return matches[0] if matches else None


def recover(manager, legacy, source, verified_original, *, before_commit=None):
    """Copy immutable snapshots; publish the saved review last, without reparsing.

    Recovery can copy large DuckDB snapshots, so authorization supplied by the
    caller is checked only after every file has been staged and again at each
    durable commit boundary. A denial removes snapshots created by this attempt
    and leaves the target review/source untouched.
    """
    with manager.lock, legacy.lock:
        with Path(verified_original).open('rb') as stream:
            digest = hashlib.file_digest(stream, 'sha256').hexdigest()
        current = None
        current_records = []
        for row in manager.list(include_samples=True):
            doc = manager.get(row['import_id'])
            if source in doc.get('native_source_bindings', []) or doc.get('sha256') == digest:
                current_records.append(doc)
                if current is None or source in doc.get('native_source_bindings', []):
                    current = doc
        # Never let list ordering select an automatic record while another
        # target record for the same bytes already contains human work.
        if any(doc.get('legacy_recovery') or
               any(sheet.get('status') in HUMAN_REVIEWED for sheet in doc.get('sheets', []))
               for doc in current_records):
            return None
        old = candidate(legacy, source, current)
        if old is None:
            return None
        if digest != old['sha256']:
            raise ValueError('知识库原件与历史核对原件不一致，未恢复或覆盖任何核对状态。')
        manifests = []
        for item in legacy.store.list_datasets():
            if item['sha256'] != digest:
                continue
            manifest = legacy.store.manifest(item['dataset_id'])
            if manifest.get('import_id') not in (None, old['import_id']):
                continue
            manifests.append(manifest)
        if old['dataset_id'] not in {item['dataset_id'] for item in manifests}:
            raise ValueError('历史核对记录的可查询快照不完整，未重新扫描或覆盖。')
        created = []
        committed = False
        try:
            # Keep every expensive copy private until it is complete. The
            # shared staging root also lets us recheck access immediately
            # before each atomic rename instead of before the long copy.
            with tempfile.TemporaryDirectory(prefix='.legacy-recover-',
                                             dir=manager.store.datasets) as temp:
                staging = Path(temp)
                pending = []
                for manifest in manifests:
                    identifier = manifest['dataset_id']
                    target = manager.store.datasets / identifier
                    original = legacy.store._path(identifier)
                    if target.exists():
                        saved = manager.store.manifest(identifier)
                        if saved['sha256'] != digest or saved['rule_version'] != manifest['rule_version']:
                            raise ValueError('目标知识库已有冲突的快照，未覆盖。')
                        continue
                    staged = staging / identifier
                    staged.mkdir()
                    with legacy.store._connect(identifier):
                        for name in ('data.duckdb', 'series.json', 'issues.json', 'source.xlsx', 'manifest.json'):
                            path = original / name
                            if path.is_symlink() or not path.is_file():
                                raise ValueError('历史快照文件不完整，未覆盖已有核对记录。')
                            shutil.copyfile(path, staged / name)
                    with (staged / 'source.xlsx').open('rb') as stream:
                        if hashlib.file_digest(stream, 'sha256').hexdigest() != digest:
                            raise ValueError('历史快照原件校验失败，未恢复。')
                    for cache in original.glob('native-charts-*.json'):
                        if cache.is_file() and not cache.is_symlink():
                            shutil.copyfile(cache, staged / cache.name)
                    # Recovery becomes the current publication in this KB,
                    # while keeping the original import time and legacy copy.
                    if identifier == old['dataset_id']:
                        recovered_manifest = {
                            **manifest,
                            'legacy_imported_at': manifest['imported_at'],
                            'imported_at': datetime.now(timezone.utc).isoformat(),
                            'legacy_recovery': {'import_id': old['import_id'], 'sha256': digest},
                        }
                        atomic_json(staged / 'manifest.json', recovered_manifest)
                    pending.append((staged, target))

                staged_source = staging / 'verified-source.xlsx'
                shutil.copyfile(verified_original, staged_source)
                with staged_source.open('rb') as stream:
                    if hashlib.file_digest(stream, 'sha256').hexdigest() != digest:
                        raise ValueError('知识库原件在恢复期间已改变，未恢复。')

                for staged, target in pending:
                    if before_commit is not None:
                        before_commit()
                    os.rename(staged, target)
                    created.append(target)

                same_record = next((doc for doc in current_records
                                    if doc.get('import_id') == old['import_id']), None)
                # Restore only the historical parser/review result. Target-side
                # metadata outside that contract (especially native bindings)
                # belongs to this knowledge base and must not be erased.
                restored = deepcopy(same_record) if same_record else {}
                for field in RECOVERED_REVIEW_FIELDS:
                    if field in old:
                        restored[field] = deepcopy(old[field])
                    else:
                        # Optional derived metadata from the target's replaced
                        # dataset must not survive as if it described the
                        # recovered historical snapshot.
                        restored.pop(field, None)
                restored['import_id'] = old['import_id']
                bindings = list((same_record or {}).get('native_source_bindings', []))
                if source not in bindings:
                    bindings.append(dict(source))
                restored['native_source_bindings'] = bindings
                restored['legacy_recovery'] = {
                    'import_id': old['import_id'], 'dataset_id': old['dataset_id'],
                    'sha256': digest, 'previous_dataset_id': (current or {}).get('dataset_id'),
                    'recovered_at': datetime.now(timezone.utc).isoformat(),
                }

                # This is the final, user-visible recovery boundary. No target
                # import/source/review mutation occurs before this recheck.
                if before_commit is not None:
                    before_commit()
                folder = manager.directory / old['import_id']
                folder.mkdir(exist_ok=True)
                destination = folder / old['source_file']
                if staged_source.exists():
                    os.replace(staged_source, destination)
                backup = folder / 'recovery-history'
                if (folder / 'review.json').exists():
                    backup.mkdir(exist_ok=True)
                    current_review = folder / 'review.json'
                    backup_name = ('before-' + hashlib.sha256(current_review.read_bytes()).hexdigest() + '.json')
                    shutil.copyfile(current_review, backup / backup_name)
                manager._save(restored)
                committed = True
                return {**manager._result(restored, reused=True), 'recovered_legacy': True,
                        'notice': '已按原件指纹恢复原空间的核对记录、可查询数据与历史来源，未重新解析。'}
        finally:
            if not committed:
                for path in created:
                    # Preserve the authorization/validation error that caused
                    # rollback; a cleanup failure must not replace it with a
                    # private filesystem detail.
                    shutil.rmtree(path, ignore_errors=True)
