"""Exercise the real archive packaging script with a native linker fixture."""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
TOOLS = ('bash', 'cc', 'ar', 'objcopy', 'objdump', 'nm')


@unittest.skipUnless(sys.platform.startswith('linux') and all(shutil.which(t) for t in TOOLS),
                     'Linux native build tools required')
class AnyDocArchiveTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.script = self.root / 'scripts/build-anydoc-lib.sh'
        self.script.parent.mkdir()
        shutil.copyfile(ROOT / 'WeKnora/scripts/build-anydoc-lib.sh', self.script)
        crate = self.root / 'third_party/anydoc-go'
        patched = crate / 'patched-anydoc'
        patched.mkdir(parents=True)
        (crate / 'Cargo.toml').write_text('anydoc = "=0.2.4"\n')
        (crate / 'version.go').write_text('const Version = "0.2.4"\n')
        (patched / 'Cargo.toml').write_text('[package]\nname = "fixture"\n')
        (patched / '.weknora-patched').write_text('0.2.4-zip9-v1\n')
        binary = self.root / 'bin'
        binary.mkdir()
        # Rust compilation is outside this packaging test. Supply its output
        # with real native code, relocations and the two optional LTO sections.
        cargo = binary / 'cargo'
        cargo.write_text('#!/bin/sh\nexit 0\n')
        cargo.chmod(0o755)
        self.env = dict(os.environ, PATH=str(binary) + os.pathsep + os.environ['PATH'],
                        TARGET='x86_64-unknown-linux-gnu', OBJCOPY=shutil.which('objcopy'))
        self.raw = crate / 'target/x86_64-unknown-linux-gnu/release/libanydoc_go.a'
        self.raw.parent.mkdir(parents=True)
        self.output = crate / 'lib/linux_amd64_gnu/libanydoc_go.a'
        (self.root / 'native.c').write_text('int bias = 2; int archive_value(void) { return 40 + bias; }\n')
        (self.root / 'bitcode').write_bytes(b'optional compiler metadata')
        self.run_cmd('cc', '-c', 'native.c', '-o', 'native.o')
        self.run_cmd('objcopy', '--add-section', '.llvmbc=bitcode',
                     '--add-section', '.llvmcmd=bitcode', 'native.o')
        self.run_cmd('ar', 'rcs', str(self.raw), 'native.o')

    def run_cmd(self, *args, **kwargs):
        return subprocess.check_output(args, cwd=self.root, stderr=subprocess.STDOUT, **kwargs)

    def test_native_code_symbols_and_relocations_survive_packaging(self):
        original = self.raw.read_bytes()
        symbols = self.run_cmd('nm', '--defined-only', str(self.raw))
        self.run_cmd('bash', str(self.script), env=self.env)
        sections = self.run_cmd('objdump', '-h', str(self.output))
        self.assertNotIn(b'.llvmbc', sections)
        self.assertNotIn(b'.llvmcmd', sections)
        self.assertEqual(symbols, self.run_cmd('nm', '--defined-only', str(self.output)))
        (self.root / 'main.c').write_text('int archive_value(void); int main(void) { return archive_value() != 42; }\n')
        self.run_cmd('cc', 'main.c', str(self.output), '-o', 'linked')
        self.run_cmd(str(self.root / 'linked'))
        self.assertEqual(original, self.raw.read_bytes())
        self.assertEqual([], list(self.output.parent.glob('.anydoc-cgo.*')))

    def test_failed_conversion_keeps_previous_archive(self):
        self.output.parent.mkdir(parents=True)
        self.output.write_bytes(b'previous successful archive')
        result = subprocess.run(['bash', str(self.script)], cwd=self.root,
                                env=dict(self.env, OBJCOPY=shutil.which('false')),
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        self.assertNotEqual(0, result.returncode)
        self.assertEqual(b'previous successful archive', self.output.read_bytes())
        self.assertEqual([], list(self.output.parent.glob('.anydoc-cgo.*')))

    def test_missing_converter_fails_with_actionable_error(self):
        result = subprocess.run(['bash', str(self.script)], cwd=self.root,
                                env=dict(self.env, OBJCOPY='missing-anydoc-objcopy'),
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        self.assertNotEqual(0, result.returncode)
        self.assertIn(b'install binutils or set OBJCOPY', result.stdout)
        self.assertFalse(self.output.exists())


if __name__ == '__main__':
    unittest.main()
