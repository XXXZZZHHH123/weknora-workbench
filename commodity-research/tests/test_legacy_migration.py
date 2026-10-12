import hashlib
from pathlib import Path
import sqlite3
import sys

from alembic import command
from alembic.config import Config
import pytest
from sqlalchemy.orm import sessionmaker

from tin.config import ROOT, SHANGHAI
from tin.db import make_engine
from tin.jobs.seed import seed_indicators, seed_judgment

sys.path.insert(0, str(ROOT / "scripts"))
from migrate_legacy import migrate, observation_digest
from weknora_api import Failure


def legacy_db(tmp_path):
    source = tmp_path / "source/data/tin.db"
    source.parent.mkdir(parents=True)
    cfg=Config();cfg.set_main_option("script_location",str(ROOT / "alembic"))
    cfg.set_main_option("sqlalchemy.url","sqlite:///"+str(source))
    command.upgrade(cfg,"b7e4c1a09d55")
    with sqlite3.connect(source) as db:
        db.execute("INSERT INTO researchers(username,display_name,varieties,status,created_at) VALUES('me','研究员','[\"SN\"]','在职','2026-09-18 00:00:00')")
    engine = make_engine("sqlite:///"+str(source))
    with sessionmaker(engine)() as session:
        seed_indicators(session);seed_judgment(session);session.commit()
    engine.dispose()
    return source


def test_migration_preserves_source_and_binds_verified_account(tmp_path):
    source = legacy_db(tmp_path)
    digest = hashlib.sha256(source.read_bytes()).hexdigest()
    destination = tmp_path / "target/workspaces/7/tin.db"
    identity = {"id": "verified-a", "email": "a@example.com", "username": "A"}
    preview = migrate(source, destination, identity)
    assert preview["mode"] == "preview" and not destination.exists()
    result = migrate(source, destination, identity, apply=True)
    assert result["verified"]
    assert hashlib.sha256(source.read_bytes()).hexdigest() == digest
    with sqlite3.connect(destination) as db:
        assert db.execute("SELECT weknora_user_id,email FROM researchers WHERE username='me'").fetchone() == ("verified-a", "a@example.com")
        assert db.execute("SELECT count(*) FROM judgments WHERE researcher_id IS NULL").fetchone()[0] == 0
        assert not db.execute("PRAGMA foreign_key_check").fetchall()
    assert migrate(source,destination,identity,apply=True)["mode"] == "already_migrated"
    with pytest.raises(Failure,match="拒绝覆盖"):
        migrate(source,destination,dict(identity,id="different-user"),apply=True)


def test_migration_detects_non_observation_changes(tmp_path):
    source=legacy_db(tmp_path);destination=tmp_path / "target/tin.db"
    identity={"id":"a","email":"a@example.com"}
    migrate(source,destination,identity,apply=True)
    with sqlite3.connect(source) as db:
        db.execute("UPDATE judgments SET tone_note='changed personal judgment'")
    with pytest.raises(Failure,match="拒绝覆盖"):
        migrate(source,destination,identity,apply=True)


def test_idempotent_migration_rechecks_destination_not_only_manifest(tmp_path):
    import shutil
    source=legacy_db(tmp_path);destination=tmp_path / "target/tin.db"
    identity={"id":"a","email":"a@example.com"}
    migrate(source,destination,identity,apply=True)
    shutil.copyfile(source,destination)
    with pytest.raises(Failure,match="清单不一致"):
        migrate(source,destination,identity,apply=True)
