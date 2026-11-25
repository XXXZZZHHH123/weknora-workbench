"""Propose and explicitly apply header edits to saved workbook mappings."""
from hashlib import sha256
import time

from .ai_review import (AIRequestBudget, HeaderCleanupModelError, _EvidenceTooLarge, _header_change_notice,
                        _verified_source_path, header_name_conflicts, suggest_header_cleanup)
from .batch_ai_import import BATCH_AI_TIMEOUT
from .model_settings import read_model
from .review_errors import ReviewValidationError


STALE_SHEET = '工作表已变化，请重新整理。'
STOP_NOTICES = {
    'cancelled': '整理已取消，已生成的建议仍可查看。',
    'timeout': '整理达到时间上限，已生成的建议仍可查看。',
    'permission_denied': '编辑权限或知识库归属已变化，整理已停止。',
}


def _mapping(sheet):
    if sheet.get('published_mapping'):
        return sheet['published_mapping']
    # Some older published profiles have no saved mapping. Their scanner
    # draft does not describe the published columns or their IDs.
    return (sheet.get('mapping') or {}) if sheet['status'] == 'confirmed' else {}


def eligible_sheets(manager, doc, *, publication=None):
    try:
        dataset_id, _ = manager.confirmation_base(doc)
    except (OSError, ValueError):
        if publication is None:
            raise
        # Keep the import detail accessible if a stored snapshot has failed.
        dataset_id = doc.get('dataset_id')
    if publication is None or dataset_id != doc.get('dataset_id'):
        publication = manager.publication_view({**doc, 'dataset_id': dataset_id})
    return [sheet for sheet in publication['sheets']
            if (sheet['status'] == 'confirmed' or sheet.get('published_in_dataset'))
            and any(len(block.get('header_rows', [])) >= 2
                    or any(len(column.get('header_cells') or []) >= 2 for column in block.get('columns', []))
                    for block in _mapping(sheet).get('blocks', []))]


def _mapping_digest(manager, sheet):
    # Check the saved review alongside the published mapping, since the latter
    # can still describe the previous version when the review has changed.
    signature = manager._mapping_signature([sheet.get('mapping'), sheet.get('published_mapping')])
    return sha256(signature.encode('utf-8')).hexdigest()


def generate(manager, import_id, *, total_max_requests, check_write, cancelled, progress):
    doc = manager.get(import_id)
    sheets = eligible_sheets(manager, doc)
    deadline = time.monotonic() + BATCH_AI_TIMEOUT
    rows = []
    used = 0
    stopped = None

    def stop_reason():
        if cancelled():
            return 'cancelled'
        if time.monotonic() >= deadline:
            return 'timeout'
        return None

    if sheets:
        try:
            path = _verified_source_path(manager, doc)
            source_error = None
        except (OSError, ValueError):
            source_error = '原文件无法核对，请检查原件后重新整理。'
        try:
            cfg = read_model(manager.root)
        except (OSError, ValueError, TypeError):
            source_error = source_error or '聊天模型设置无法读取，请检查后重试。'

    def notify(completed, name):
        progress({'completed': completed, 'total': len(sheets), 'sheet': name,
                  'stage': '核对表头说明', 'requests_used': used,
                  'total_max_requests': total_max_requests})

    for index, sheet in enumerate(sheets):
        stopped = stop_reason()
        if stopped:
            break
        try:
            check_write()
        except Exception:
            stopped = 'permission_denied'
            break
        name = sheet['name']
        row = {'sheet_name': name, 'fingerprint': sheet['fingerprint'],
               'mapping_digest': _mapping_digest(manager, sheet), 'changes': []}
        notify(index, name)
        if source_error:
            row.update(status='failed', error=source_error)
        elif used >= total_max_requests:
            row.update(status='failed', error='本次模型请求次数已用完，这张表尚未整理。')
        else:
            budget = AIRequestBudget(min(2, total_max_requests - used))
            try:
                proposal = suggest_header_cleanup(path, cfg, sheet, _mapping(sheet), budget, stop_reason,
                                                   deadline=deadline)
                changes, diagnostics = proposal['changes'], proposal['diagnostics']
                status = 'rename' if changes else ('needs_review' if diagnostics else 'unchanged')
                row.update(status=status, changes=changes, diagnostics=diagnostics,
                           notices=proposal['notices'], mapping=proposal['mapping'])
            except ReviewValidationError:
                row.update(status='failed', error='表头建议未通过证据核对，请核对原表后重试。')
            except HeaderCleanupModelError as exc:
                row.update(status='failed', error=str(exc))
            except _EvidenceTooLarge:
                row.update(status='failed', error='模型请求过大，请减少本次核对范围。')
            except Exception:
                row.update(status='failed', error='未能取得可用的表头建议，请检查模型设置后重试。')
            finally:
                used += budget.used
        stopped = stop_reason()
        if stopped:
            break
        rows.append(row)
        notify(index + 1, name)

    if stopped:
        rows.extend({'sheet_name': sheet['name'], 'status': 'failed', 'changes': [],
                     'fingerprint': sheet['fingerprint'], 'mapping_digest': _mapping_digest(manager, sheet),
                     'error': STOP_NOTICES[stopped]} for sheet in sheets[len(rows):])
    return {'import_id': import_id, 'source_sha256': doc['sha256'], 'sheets': rows,
            'requests_used': used, 'total_max_requests': total_max_requests, 'stopped': stopped,
            'notice': STOP_NOTICES.get(stopped, '表头说明整理完成，请核对建议。')}


def apply(manager, import_id, result, sheets, *, task, check_write):
    doc = manager.get(import_id)
    source_matches = doc['sha256'] == result['source_sha256']
    if source_matches:
        try:
            _verified_source_path(manager, doc)
        except (OSError, ValueError):
            source_matches = False
    current = {sheet['name']: sheet for sheet in eligible_sheets(manager, doc)}
    dataset_id = doc.get('dataset_id')
    renamed = 0
    rows = []
    for proposal in sheets:
        name = proposal['sheet_name']
        row = {'sheet_name': name, 'status': 'failed'}
        rows.append(row)
        sheet = current.get(name)
        if (not source_matches or sheet is None or sheet['fingerprint'] != proposal['fingerprint']
                or _mapping_digest(manager, sheet) != proposal['mapping_digest']):
            row['error'] = STALE_SHEET
            continue
        mapping = proposal['mapping']
        if header_name_conflicts(_mapping(sheet), mapping):
            row['error'] = '改名后出现重名，请重新整理'
            continue
        try:
            check_write()
            review = task('review', import_id=import_id, mapping=mapping)
        except Exception:
            row['error'] = '未能完成工作表检查，请稍后重试。'
            continue
        if review.get('status') != 'confirmed':
            row['error'] = '建议未通过工作表完整检查，请核对原表后重试。'
            continue
        try:
            published = task('confirm', import_id=import_id, mapping=mapping,
                             save_template=False, publish_guard=check_write)
        except Exception:
            row['error'] = '工作表未能重新导入，请稍后重试。'
            continue
        count = len(proposal['changes'])
        renamed += count
        dataset_id = published['dataset_id']
        row.update(status='applied', renamed_fields=count)
    return {'sheets': rows, 'dataset_id': dataset_id, 'renamed_fields': renamed,
            'notice': _header_change_notice(renamed, applied=True)}
