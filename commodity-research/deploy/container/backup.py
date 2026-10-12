"""Stream a research-data archive with SQLite online backups (including WAL commits).

Each database is a consistent snapshot. Other files are copied at archive time;
stop writes for a coordinated snapshot of database plus uploads/exports.
"""
import argparse
from pathlib import Path
import sqlite3
import sys
import tarfile
import tempfile


def archive(data_dir: Path, destination):
    if not data_dir.is_dir():
        raise ValueError("Research data directory is missing")
    with tempfile.TemporaryDirectory(prefix="research-backup-") as temp:
        snapshots = Path(temp)
        database_paths = set()
        for source in sorted(data_dir.rglob("*.db")):
            if source.is_symlink() or not source.is_file():
                raise ValueError("Database must be a regular file")
            relative = source.relative_to(data_dir)
            target = snapshots / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            with sqlite3.connect(source.as_uri() + "?mode=ro", uri=True, timeout=30) as reader:
                with sqlite3.connect(target) as writer:
                    reader.backup(writer)
                    if writer.execute("PRAGMA integrity_check").fetchone()[0] != "ok":
                        raise ValueError("SQLite backup failed integrity check")
            database_paths.add(relative)
        with tarfile.open(fileobj=destination, mode="w|gz") as bundle:
            for relative in sorted(database_paths):
                bundle.add(snapshots / relative, arcname=str(relative), recursive=False)
            for source in sorted(data_dir.rglob("*")):
                relative = source.relative_to(data_dir)
                if (relative in database_paths or source.name.endswith(("-wal", "-shm"))
                        or source.name == "service-token" or source.is_symlink()):
                    continue
                # All directory entries are enumerated; never recursively add live DBs.
                bundle.add(source, arcname=str(relative), recursive=False)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--data-dir", type=Path, default=Path("/data"))
    args = parser.parse_args()
    archive(args.data_dir.resolve(), sys.stdout.buffer)
