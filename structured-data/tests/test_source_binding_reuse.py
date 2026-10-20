from concurrent.futures import Future, ThreadPoolExecutor
from copy import deepcopy
from threading import Event
import time

import pytest

from structured_data import source_context as context
from structured_data import source_binding_reuse as reuse_module
from structured_data.source_binding_reuse import SourceBindingReuse
from structured_data.workspaces import Identity, current_identity


class Store:
    def __init__(self, root):
        self.root = root
        self.version = 'snapshot-a'
        self.rows = [{'series_id': name, 'sheet': 'Sheet', 'metric': 'Measure',
                      'unit': 'units', 'header_cells': [name + '1'],
                      'dimensions': {'source': name}, 'source_context': {'version': 1}}
                     for name in ['B', 'C']]

    def catalog(self, dataset):
        return deepcopy(self.rows)

    def manifest(self, dataset):
        return {'dataset_id': dataset, 'sha256': self.version}


@pytest.fixture
def setup(tmp_path, monkeypatch):
    monkeypatch.setattr(context, '_binding_reuse', SourceBindingReuse())
    monkeypatch.setattr('structured_data.model_settings.read_model', lambda root: {})
    store = Store(tmp_path)
    calls = []

    def model(request):
        calls.append(deepcopy(request))
        return {'decision': 'resolved', 'bindings': [
            {'considered_ids': ['B', 'C'], 'selected_ids': request['requested_series_ids'],
             'reason': 'The complete request selects these actual sources.'}]}

    return store, model, calls


def verify(setup, ids=None, question='Full request'):
    store, model, _ = setup
    return context.verify_selection(store, 'dataset', ids or ['B'], question, model)


def test_exact_success_reused_but_current_catalogue_and_validator_are_still_required(setup, monkeypatch):
    store, _, calls = setup
    assert verify(setup) is None
    assert verify(setup) is None
    assert len(calls) == 1
    original = context._validate_binding
    validations = []
    def validate(*args, **kwargs):
        validations.append(True)
        return original(*args, **kwargs)
    monkeypatch.setattr(context, '_validate_binding', validate)
    assert verify(setup) is None
    assert validations == [True]
    store.rows = store.rows[1:]
    with pytest.raises(ValueError, match='不存在'):
        verify(setup)
    assert len(calls) == 1


@pytest.mark.parametrize('change', ['question', 'ids', 'order', 'snapshot', 'evidence', 'workspace', 'prompt'])
def test_identity_or_semantics_changes_require_new_model_review(setup, monkeypatch, change):
    store, _, calls = setup
    assert verify(setup, ids=['B', 'C']) is None
    kwargs = {'ids': ['B', 'C']}
    if change == 'question': kwargs['question'] = 'Full request with changed conversation'
    elif change == 'ids': kwargs['ids'] = ['C']
    elif change == 'order': kwargs['ids'] = ['C', 'B']
    elif change == 'snapshot': store.version = 'snapshot-b'
    elif change == 'evidence': store.rows[1]['source_context']['version'] = 2
    elif change == 'workspace': store.root = store.root / 'other-owner'
    elif change == 'prompt': monkeypatch.setattr(context, 'SOURCE_BINDING_PROMPT', context.SOURCE_BINDING_PROMPT + '\nchanged')
    assert verify(setup, **kwargs) is None
    assert len(calls) == 2


def test_model_and_credential_changes_invalidate_reuse_without_exposing_config(setup, monkeypatch):
    config = {'model': 'model-a', 'base_url': 'https://example.test', 'api_key': 'secret-a'}
    monkeypatch.setattr('structured_data.model_settings.read_model', lambda root: dict(config))
    assert verify(setup) is None
    config['api_key'] = 'secret-b'
    assert verify(setup) is None
    config['model'] = 'model-b'
    assert verify(setup) is None
    assert len(setup[2]) == 3
    assert 'secret-' not in str(list(context._binding_reuse._entries))


def test_owner_identity_is_not_shared_even_with_same_root(setup):
    for owner in ['owner-a', 'owner-b', 'owner-a']:
        token = current_identity.set(Identity(owner, '', '', 1, 'owner'))
        try:
            assert verify(setup) is None
        finally:
            current_identity.reset(token)
    assert len(setup[2]) == 2


def test_different_model_callable_does_not_inherit_success(setup):
    store, model, calls = setup
    assert verify(setup) is None
    def alternative(request):
        return model(request)
    assert context.verify_selection(store, 'dataset', ['B'], 'Full request', alternative) is None
    assert len(calls) == 2


@pytest.mark.parametrize('decision', ['clarify', 'plan_mismatch', 'invalid', 'error'])
def test_unsuccessful_reviews_are_never_cached(setup, decision):
    store, _, calls = setup
    def model(request):
        calls.append(request)
        if decision == 'error': raise ValueError('temporary provider error')
        if decision == 'invalid': return {'decision': 'resolved', 'bindings': []}
        return {'decision': 'clarify' if decision == 'clarify' else 'resolved',
                'question': 'Which actual source should be used?',
                'bindings': [{'considered_ids': ['B', 'C'], 'selected_ids': ['C'],
                              'reason': 'Current semantic decision.'}]}
    for _ in range(2):
        result = context.verify_selection(store, 'dataset', ['B'], 'Full request', model)
        assert result['status'] != 'ok'
        if result['status'] == 'error':
            assert result['code'] == 'source_binding_unavailable'
        else:
            assert 'code' not in result
    assert len(calls) == (4 if decision == 'invalid' else 2)
    assert not context._binding_reuse._entries


def test_config_change_during_model_call_does_not_cache_old_key(setup, monkeypatch):
    store, model, calls = setup
    config = {'model': 'before'}
    monkeypatch.setattr('structured_data.model_settings.read_model', lambda root: dict(config))
    def changing(request):
        config['model'] = 'after'
        return model(request)
    assert context.verify_selection(store, 'dataset', ['B'], 'Full request', changing) is None
    assert not context._binding_reuse._entries
    assert len(calls) == 1


@pytest.mark.parametrize('fails', [False, True])
def test_concurrent_same_binding_is_singleflight_and_failure_can_be_retried_later(setup, monkeypatch, fails):
    store, model, calls = setup
    entered, release, joined = Event(), Event(), Event()
    class ObservedFuture(Future):
        def result(self, timeout=None):
            joined.set()
            return super().result(timeout)
    monkeypatch.setattr(reuse_module, 'Future', ObservedFuture)
    def slow(request):
        entered.set()
        assert release.wait(5)
        if fails:
            calls.append(request)
            raise ValueError('temporary provider error')
        return model(request)
    with ThreadPoolExecutor(max_workers=2) as pool:
        first = pool.submit(context.verify_selection, store, 'dataset', ['B'], 'Full request', slow)
        assert entered.wait(5)
        second = pool.submit(context.verify_selection, store, 'dataset', ['B'], 'Full request', slow)
        assert joined.wait(5)
        release.set()
        a, b = first.result(5), second.result(5)
    assert a == b
    assert len(calls) == 1
    if fails:
        assert a['status'] == 'error'
        assert context.verify_selection(store, 'dataset', ['B'], 'Full request', slow)['status'] == 'error'
        assert len(calls) == 2
    else:
        assert a is None


def test_ttl_lru_and_copy_isolation():
    now = [0]
    cache = SourceBindingReuse(capacity=2, ttl=10, clock=lambda: now[0])
    calls = []
    def compute():
        calls.append(True)
        return {'bindings': [{'selected_ids': ['B']}]}
    def run(key): return cache.run(key, compute, lambda value: True, 20)
    first = run('a')
    first['bindings'][0]['selected_ids'].append('forged')
    assert run('a')['bindings'][0]['selected_ids'] == ['B']
    run('b'); run('a'); run('c')
    assert list(cache._entries) == ['a', 'c']
    run('b')
    assert len(calls) == 4
    now[0] = 11
    run('b')
    assert len(calls) == 5


def test_overlarge_success_is_not_retained():
    cache = SourceBindingReuse()
    for _ in range(2):
        cache.run('a', lambda: {'value': 'x' * 32769}, lambda value: True, time.monotonic() + 1)
    assert not cache._entries


def test_waiter_timeout_does_not_cancel_owner_or_cache_failure():
    cache = SourceBindingReuse()
    entered, release = Event(), Event()
    def compute():
        entered.set()
        assert release.wait(5)
        return {'value': 'success'}
    with ThreadPoolExecutor(max_workers=1) as pool:
        first = pool.submit(cache.run, 'a', compute, lambda value: True, time.monotonic() + 5)
        assert entered.wait(5)
        with pytest.raises(ValueError, match='时间预算'):
            cache.run('a', compute, lambda value: True, time.monotonic() - 1)
        release.set()
        assert first.result(5) == {'value': 'success'}
    assert cache.run('a', lambda: pytest.fail('must reuse success'), lambda value: True, time.monotonic() + 1) == {'value': 'success'}
