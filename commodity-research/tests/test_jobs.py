import json
from contextlib import nullcontext
from types import SimpleNamespace

from tests.conftest import D
from tin.jobs import __main__ as jobs


def test_verified_snapshots_use_different_space_and_user_paths(tmp_path, monkeypatch):
    from tin.integration.identity import Identity, current_identity
    from tin.ownership import scoped_directory
    monkeypatch.setattr(jobs.settings, "exports_dir", tmp_path / "exports")
    monkeypatch.setattr(jobs, "SessionLocal", lambda: nullcontext(object()))
    monkeypatch.setattr(jobs, "latest_trade_date", lambda _session: D)
    paths=[]
    for identity in (Identity("a","a@example.com","A",7,"owner"),
                     Identity("b","b@example.com","B",7,"owner"),
                     Identity("a","a@example.com","A",8,"owner")):
        marker=current_identity.set(identity)
        try:
            monkeypatch.setattr(jobs, "snapshot", lambda *_args: {"owner": identity.user_id, "space": identity.tenant_id})
            jobs.cmd_snapshot(SimpleNamespace(date=None))
            output=scoped_directory(jobs.settings.exports_dir, personal=True) / "SN" / f"{D.isoformat()}.json"
            assert json.loads(output.read_text()) == {"owner":identity.user_id,"space":identity.tenant_id}
            paths.append(output)
        finally:
            current_identity.reset(marker)
    assert len(set(paths)) == 3
    assert json.loads(paths[0].read_text())["owner"] == "a"


def test_snapshot_supports_exports_outside_project(tmp_path, monkeypatch, capsys):
    exports = tmp_path / "var" / "lib" / "commodity-research-platform" / "exports"
    monkeypatch.setattr(jobs.settings, "exports_dir", exports)
    monkeypatch.setattr(jobs, "SessionLocal", lambda: nullcontext(object()))
    monkeypatch.setattr(jobs, "latest_trade_date", lambda _session: D)
    monkeypatch.setattr(jobs, "snapshot", lambda _session, variety, day: {
        "variety": variety,
        "as_of": day.isoformat(),
    })

    jobs.cmd_snapshot(SimpleNamespace(date=None))

    output = exports / "SN" / f"{D.isoformat()}.json"
    assert json.loads(output.read_text(encoding="utf-8")) == {
        "variety": "SN",
        "as_of": D.isoformat(),
    }
    assert str(output) in capsys.readouterr().out
