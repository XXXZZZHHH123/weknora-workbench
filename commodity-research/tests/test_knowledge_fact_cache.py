"""Large fact sets must be reusable without a live workbench connection."""
from tests.test_knowledge_facts import canonical, api, FIRST, SECOND
from concurrent.futures import ThreadPoolExecutor
import threading


def close_projection_connections():
    from tin.integration import kb_facts
    with kb_facts._projection_lock:
        for engine in kb_facts._projection_engines.values():
            engine.dispose()
        kb_facts._projection_engines.clear()


def test_same_knowledge_version_is_built_once_across_workbenches(canonical):
    client, headers, state, _ = canonical
    assert client.get(api(FIRST), headers=headers).json()['total'] == 1
    pages = state['pages']
    assert client.get(api(SECOND), headers=headers).json()['total'] == 1
    assert state['pages'] == pages


def test_reopening_all_connections_does_not_download_current_facts_again(canonical):
    client, headers, state, _ = canonical
    assert client.get(api(), headers=headers).json()['total'] == 1
    pages = state['pages']
    close_projection_connections()
    assert client.get(api(), headers=headers).json()['total'] == 1
    assert state['pages'] == pages


def test_expired_connection_reuses_current_version_after_fresh_verification(canonical, monkeypatch):
    from tin.integration import kb_facts
    client, headers, state, _ = canonical
    monkeypatch.setattr(kb_facts, 'PROJECTION_SECONDS', 0)
    assert client.get(api(), headers=headers).json()['total'] == 1
    pages = state['pages']
    calls = len(state['calls'])
    assert client.get(api(), headers=headers).json()['total'] == 1
    assert state['pages'] == pages
    assert len(state['calls']) > calls


def test_concurrent_workbenches_share_one_complete_build(canonical, monkeypatch):
    from tin.integration import kb_facts
    client, headers, state, _ = canonical
    entered, second_verified, release = threading.Event(), threading.Event(), threading.Event()
    install, version = kb_facts.install_projection, kb_facts.current_version
    builds = []

    def build(*args):
        builds.append(1)
        entered.set()
        assert release.wait(10)
        return install(*args)

    def checked(scope, sources):
        result = version(scope, sources)
        if scope.workbench_id == SECOND:
            second_verified.set()
        return result

    monkeypatch.setattr(kb_facts, 'install_projection', build)
    monkeypatch.setattr(kb_facts, 'current_version', checked)
    with ThreadPoolExecutor(max_workers=2) as pool:
        first = pool.submit(client.get, api(FIRST), headers=headers)
        assert entered.wait(10)
        second = pool.submit(client.get, api(SECOND), headers=headers)
        try:
            assert second_verified.wait(10)
        finally:
            release.set()
        assert first.result(timeout=15).json()['total'] == 1
        assert second.result(timeout=15).json()['total'] == 1
    assert len(builds) == 1
    assert state['pages'] == 1


def test_waiting_request_rechecks_workbench_binding_before_using_shared_build(canonical, monkeypatch):
    from tin.integration import kb_facts
    client, headers, state, _ = canonical
    entered, second_verified, release = threading.Event(), threading.Event(), threading.Event()
    install, version = kb_facts.install_projection, kb_facts.current_version

    def build(*args):
        entered.set()
        assert release.wait(10)
        return install(*args)

    def checked(scope, sources):
        result = version(scope, sources)
        if scope.workbench_id == SECOND:
            second_verified.set()
        return result

    monkeypatch.setattr(kb_facts, 'install_projection', build)
    monkeypatch.setattr(kb_facts, 'current_version', checked)
    with ThreadPoolExecutor(max_workers=2) as pool:
        first = pool.submit(client.get, api(FIRST), headers=headers)
        assert entered.wait(10)
        second = pool.submit(client.get, api(SECOND), headers=headers)
        try:
            assert second_verified.wait(10)
            state['second_other_kb'] = True
        finally:
            release.set()
        assert first.result(timeout=15).status_code == 200
        assert second.result(timeout=15).status_code == 409
