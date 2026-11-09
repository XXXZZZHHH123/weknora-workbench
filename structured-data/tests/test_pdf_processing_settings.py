from dataclasses import FrozenInstanceError
import pytest
from structured_data.pdf_processing_settings import PDFProcessingSettings, current_settings, use_settings
from structured_data.pdf_automation import _request_budget, _overlapping_batches, grouping_evidence
from test_pdf_automation import _large_grouping_draft


def test_policy_snapshot_is_immutable_and_nested_scopes_restore(monkeypatch):
    from structured_data.pdf_parser_bridge import ParserConfig
    monkeypatch.setenv('PDF_TABLE_PARSER_ENGINE', 'remote_vlm')
    monkeypatch.setenv('PDF_TABLE_PARSER_URL', 'http://remote.test')
    policy = PDFProcessingSettings(overlap_pages=2, grouping_request_bytes=48000, queue_timeout_seconds=7200, execution_timeout_seconds=900)
    with use_settings(policy):
        assert _request_budget() == 48000
        config = ParserConfig.from_env()
        assert config.timeout_seconds == 900 and config.queue_timeout_seconds == 7200
        with pytest.raises(FrozenInstanceError): policy.overlap_pages = 3
        with use_settings(PDFProcessingSettings()): assert _request_budget() == 24000
        assert current_settings.get() is policy
    assert current_settings.get() is None


@pytest.mark.parametrize('values', [{'overlap_pages':0},{'overlap_pages':6},{'overlap_pages':True},
    {'grouping_request_bytes':11999},{'execution_timeout_seconds':7201},{'queue_timeout_seconds':86401}])
def test_invalid_snapshots_do_not_start_work(values):
    with pytest.raises(ValueError): PDFProcessingSettings(**values)


def test_two_page_overlap_preserves_complete_boundary_evidence():
    draft = _large_grouping_draft()
    # Enlarge whole-page narrative so only three complete pages fit each batch.
    for context in draft['bundle']['page_context']:
        context['text'] += ' original boundary evidence ' * 45
    with use_settings(PDFProcessingSettings(overlap_pages=2, grouping_request_bytes=12000)):
        evidence = grouping_evidence(draft['bundle'])
        batches = _overlapping_batches(evidence)
        assert len(batches) > 1
        assert set.union(*batches) == {1, 2, 3, 4}
        assert all(len(left & right) == 2 for left, right in zip(batches, batches[1:]))
