"""Opening an existing workbench read session never takes the SQLite write lock."""
from dataclasses import replace

from sqlalchemy import event

from tin.config import settings
from tin.integration import kb_facts, workbench_scope
from tin.integration.identity import Identity
from tin.integration.workbench_scope import WorkbenchScope


def test_only_the_first_open_registers_the_workbench_scope(tmp_path, monkeypatch):
    monkeypatch.setattr(settings, 'data_dir', tmp_path)
    scope = WorkbenchScope(Identity('owner', '', 'Fixture', 17, 'owner'), '7ac25ec4-378c-4071-a8ee-c3679617b67a',
        'fixture-kb', 17, {'workspace_mode': 'isolated', 'can_edit': True, 'can_read_source': True}, 'Bearer fixture')
    scope = replace(scope, scope_version=2)
    monkeypatch.setattr(kb_facts, 'projected_session', lambda bind, scope, **kwargs: type('S', (), {'info': {}})())
    engine = workbench_scope.isolated_session_factory(17, scope.workbench_id).kw['bind']
    writes = []
    def watch(_conn, _cursor, sql, *_):
        if sql.lstrip().upper().startswith(('INSERT', 'UPDATE', 'DELETE')):
            writes.append(sql)
    event.listen(engine, 'before_cursor_execute', watch)
    try:
        workbench_scope.open_workbench_session(scope)
        first = list(writes)
        workbench_scope.open_workbench_session(scope)
    finally:
        event.remove(engine, 'before_cursor_execute', watch)
    assert len(first) == 1 and 'isolated_scope' in first[0]
    assert writes == first, 'A later open must only read the registered scope'
