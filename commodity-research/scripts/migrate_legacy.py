#!/usr/bin/env python3
"""Preview or migrate one legacy SQLite research workspace, preserving the source."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import sys
import tempfile

from alembic import command
from alembic.config import Config
from alembic.script import ScriptDirectory

from weknora_api import API, Failure

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_OWNER = "eval_a_28c1c2679d@example.com"


def counts(connection):
    return {name: connection.execute('SELECT count(*) FROM "' + name + '"').fetchone()[0]
            for (name,) in connection.execute("SELECT name FROM sqlite_master WHERE type='table'")
            if name != "alembic_version"}


def observation_digest(connection, max_id=None):
    digest = hashlib.sha256()
    query = "SELECT * FROM observations" + (" WHERE id <= ?" if max_id is not None else "") + " ORDER BY id"
    for row in connection.execute(query, (max_id,) if max_id is not None else ()):
        digest.update(json.dumps(row, ensure_ascii=False, separators=(",", ":")).encode())
        digest.update(b"\n")
    return digest.hexdigest()


def source_digest(connection):
    digest = hashlib.sha256()
    for statement in connection.iterdump():
        digest.update(statement.encode())
        digest.update(b"\n")
    return digest.hexdigest()


def migrate(source, destination, identity, *, apply=False):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if not source.is_file() or source == destination:
        raise Failure("来源数据库无效或与目标相同")
    owner_id, owner_email = str(identity["id"]), identity["email"]
    uri = source.as_uri() + "?mode=ro"
    with sqlite3.connect(uri, uri=True) as original:
        before = counts(original)
        digest = observation_digest(original)
        full_digest = source_digest(original)
        max_id = original.execute("SELECT max(id) FROM observations").fetchone()[0] or 0
        if original.execute("PRAGMA foreign_key_check").fetchall():
            raise Failure("原数据库存在外键错误，拒绝迁移")
    result = {"mode": "apply" if apply else "preview", "source": str(source),
              "destination": str(destination), "owner_email": owner_email,
              "owner_id": owner_id, "counts": before, "observation_sha256": digest,
              "source_content_sha256": full_digest}
    if not apply:
        return result
    manifest_path = destination.parent / "migration.json"
    if destination.exists():
        previous = json.loads(manifest_path.read_text()) if manifest_path.is_file() else {}
        if (previous.get("source") == str(source) and previous.get("owner_id") == owner_id
                and previous.get("source_content_sha256") == full_digest):
            try:
                cfg = Config(); cfg.set_main_option("script_location", str(ROOT / "alembic"))
                expected_heads = set(ScriptDirectory.from_config(cfg).get_heads())
                with sqlite3.connect(destination.as_uri() + "?mode=ro", uri=True) as target:
                    actual_heads = {row[0] for row in target.execute("SELECT version_num FROM alembic_version")}
                    binding = target.execute("SELECT weknora_user_id,email FROM researchers WHERE username='me'").fetchone()
                    retained = counts(target)
                    valid = (actual_heads == expected_heads and binding == (owner_id, owner_email)
                             and all(retained.get(table, -1) >= count for table, count in before.items())
                             and observation_digest(target, max_id) == digest
                             and not target.execute("PRAGMA foreign_key_check").fetchall())
                if valid:
                    return result | {"mode": "already_migrated", "writes": False, "verified": True}
            except sqlite3.Error:
                pass
            raise Failure("目标库与迁移清单不一致；请核对备份及账号绑定，拒绝跳过或覆盖")
        raise Failure("目标数据库已经存在且不是同一已验证迁移，拒绝覆盖")
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.parent.chmod(0o700)
    with tempfile.TemporaryDirectory(prefix=".legacy-migration-", dir=destination.parent) as temporary:
        copy = Path(temporary) / "tin.db"
        with sqlite3.connect(uri, uri=True) as original, sqlite3.connect(copy) as target:
            original.backup(target)
            snapshot_counts = counts(target)
            snapshot_digest = observation_digest(target)
            full_digest = source_digest(target)
            max_id = target.execute("SELECT max(id) FROM observations").fetchone()[0] or 0
        # SQLite backup captures live WAL consistently. Recheck the actual snapshot.
        result.update(counts=snapshot_counts, observation_sha256=snapshot_digest,
                      source_content_sha256=full_digest, source_max_observation_id=max_id)
        cfg = Config()
        cfg.set_main_option("script_location", str(ROOT / "alembic"))
        cfg.set_main_option("sqlalchemy.url", "sqlite:///" + str(copy))
        command.upgrade(cfg, "head")
        with sqlite3.connect(copy) as target:
            target.execute("PRAGMA foreign_keys=ON")
            me = target.execute("SELECT id, weknora_user_id FROM researchers WHERE username='me'").fetchone()
            if not me:
                raise Failure("旧库没有唯一默认研究员 me，请明确人工归属")
            researcher_id, existing_user = me
            if existing_user not in (None, owner_id):
                raise Failure("默认研究员已绑定另一个账号，拒绝改写归属")
            duplicate = target.execute("SELECT id FROM researchers WHERE weknora_user_id=? AND id<>?",
                                       (owner_id, researcher_id)).fetchone()
            if duplicate:
                raise Failure("目标账号已有另一个研究员档案，拒绝自动合并")
            target.execute("UPDATE researchers SET weknora_user_id=?, email=?, display_name=? WHERE id=?",
                           (owner_id, owner_email, identity.get("username") or owner_email, researcher_id))
            # Legacy NULL records came from the one default researcher; only this explicit migration claims them.
            target.execute("UPDATE judgments SET researcher_id=? WHERE researcher_id IS NULL", (researcher_id,))
            target.execute("UPDATE strategies SET researcher_id=? WHERE researcher_id IS NULL", (researcher_id,))
            if any(counts(target).get(table) != count for table, count in snapshot_counts.items()):
                raise Failure("迁移前后记录数量不一致")
            if observation_digest(target) != snapshot_digest or target.execute("PRAGMA foreign_key_check").fetchall():
                raise Failure("迁移前后原观测或外键核对失败")
            target.commit()
        copy.chmod(0o600)
        # Persist full legacy files as evidence; integrated pages regenerate user-scoped reports.
        legacy = Path(temporary) / "legacy"
        for name in ("exports", "raw"):
            path = source.parent.parent / name
            if path.is_dir():
                shutil.copytree(path, legacy / name)
        staging = source.parent / "import_staging"
        if staging.is_dir():
            shutil.copytree(staging, legacy / "import_staging")
        if legacy.exists():
            shutil.move(str(legacy), str(destination.parent / "legacy"))
        result.update(verified=True, migrated_at=datetime.now(timezone.utc).isoformat())
        temporary_manifest = Path(temporary) / "migration.json"
        temporary_manifest.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
        temporary_manifest.chmod(0o600)
        # DB publication is last; a failed staging operation never exposes partial migrated data.
        os.replace(temporary_manifest, manifest_path)
        os.replace(copy, destination)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--data-dir", type=Path, default=ROOT / "data")
    parser.add_argument("--api", default="http://127.0.0.1:18081")
    parser.add_argument("--accounts", type=Path, default=ROOT.parent / ".private/accounts.json")
    parser.add_argument("--owner-email", default=DEFAULT_OWNER)
    parser.add_argument("--tenant", type=int, help="Explicit authorized target workspace; default current workspace")
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    try:
        api = API(args.api, args.tenant)
        verified = api.login_email(args.accounts, args.owner_email)
        destination = args.data_dir / "workspaces" / str(api.tenant) / "tin.db"
        # A shared OS lock serializes migration with first-request workspace initialization.
        destination.parent.mkdir(parents=True, exist_ok=True) if args.apply else None
        if args.apply:
            import fcntl
            with destination.with_suffix(".init.lock").open("a") as lock:
                fcntl.flock(lock, fcntl.LOCK_EX)
                result = migrate(args.source, destination, verified["user"], apply=True)
        else:
            result = migrate(args.source, destination, verified["user"])
        print(json.dumps(result, ensure_ascii=False, indent=2))
    except (Failure, OSError, ValueError, KeyError) as exc:
        print(str(exc) if isinstance(exc, Failure) else "本地数据或响应验证失败，未输出凭据及原始异常", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
