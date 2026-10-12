import importlib.util
import io
from pathlib import Path
import sqlite3
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('research_backup', ROOT / 'commodity-research/deploy/container/backup.py')
backup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(backup)


class ResearchBackupTests(unittest.TestCase):
    def test_live_wal_commits_are_included_and_secrets_are_excluded(self):
        with tempfile.TemporaryDirectory() as temp:
            data = Path(temp) / 'data'
            workspace = data / 'workspaces/10000'
            workspace.mkdir(parents=True)
            database = workspace / 'tin.db'
            with sqlite3.connect(database) as connection:
                connection.execute('PRAGMA journal_mode=WAL')
                connection.execute('CREATE TABLE evidence (value TEXT)')
                connection.execute("INSERT INTO evidence VALUES ('committed in WAL')")
                connection.commit()
                self.assertTrue(Path(str(database) + '-wal').exists())
                (workspace / 'source.txt').write_text('source evidence')
                (data / 'service-token').write_text('secret')
                stream = io.BytesIO()
                backup.archive(data, stream)
                stream.seek(0)
                with tarfile.open(fileobj=stream, mode='r:gz') as archive:
                    names = archive.getnames()
                    self.assertIn('workspaces/10000/tin.db', names)
                    self.assertNotIn('service-token', names)
                    self.assertFalse(any(name.endswith(('-wal', '-shm')) for name in names))
                    restore = Path(temp) / 'restored.db'
                    restore.write_bytes(archive.extractfile('workspaces/10000/tin.db').read())
                    self.assertEqual(archive.extractfile('workspaces/10000/source.txt').read(), b'source evidence')
                with sqlite3.connect(restore) as restored:
                    self.assertEqual(restored.execute('SELECT value FROM evidence').fetchall(), [('committed in WAL',)])
                    self.assertEqual(restored.execute('PRAGMA integrity_check').fetchone()[0], 'ok')
