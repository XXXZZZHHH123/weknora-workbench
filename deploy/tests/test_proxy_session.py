"""Use real Linux flock to verify concurrent, failed and interrupted lifetimes."""
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile
import time
import unittest

SCRIPTS = Path(__file__).resolve().parents[1]


@unittest.skipUnless(shutil.which('flock'), 'requires real Linux flock')
class ProxySessionTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory()
        self.root=Path(self.temp.name)
        self.bin=self.root/'bin';self.bin.mkdir()
        shutil.copy(SCRIPTS/'proxy-session.sh',self.bin/'proxy-session.sh')
        self.env=dict(os.environ,HOME=str(self.root),PATH=str(self.bin)+':'+os.environ['PATH'],PROXY_TEST_ROOT=str(self.root))
        self.program('linux-proxy.sh','''#!/bin/sh
if [ "$(cat "$PROXY_TEST_ROOT/state" 2>/dev/null)" != running ]; then
 printf running > "$PROXY_TEST_ROOT/state"
 printf 'start\\n' >> "$PROXY_TEST_ROOT/events"
fi
''')
        self.program('docker','''#!/usr/bin/env python3
import os,sys
from pathlib import Path
r=Path(os.environ['PROXY_TEST_ROOT']);a=sys.argv[1:]
if a[0]=='inspect': print((r/'state').read_text() if (r/'state').exists() else 'exited')
elif a[0]=='stop':
 (r/'state').write_text('exited')
 with (r/'events').open('a') as f:f.write('stop\\n')
elif a[0]!='update': sys.exit(1)
''')

    def tearDown(self):
        # Watchers should finish independently once their last lease disappears.
        time.sleep(1.2)
        self.temp.cleanup()

    def program(self,name,body):
        p=self.bin/name;p.write_text(body);p.chmod(0o700)

    def wait_for(self,predicate):
        until=time.monotonic()+8
        while time.monotonic()<until:
            if predicate():return
            time.sleep(.05)
        self.fail('timed out waiting for proxy lifecycle')

    def launch(self,name):
        code='''import pathlib,time,sys,os
# Match the separate locks opened by poll.sh and deploy.sh.
for descriptor in (8,9):
    os.dup2(os.open(os.devnull,os.O_WRONLY),descriptor)
r=pathlib.Path(sys.argv[1]);name=sys.argv[2]
(r/(name+'.ready')).touch()
while not (r/(name+'.finish')).exists():time.sleep(.05)
'''
        return subprocess.Popen(['bash',str(self.bin/'proxy-session.sh'),'python3','-c',code,str(self.root),name],
                                env=self.env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,start_new_session=True)

    def test_concurrent_callers_only_start_once_and_last_exit_stops(self):
        a=self.launch('a');self.wait_for(lambda:(self.root/'a.ready').exists())
        b=self.launch('b');self.wait_for(lambda:(self.root/'b.ready').exists())
        (self.root/'a.finish').touch();a.communicate(timeout=8)
        self.assertEqual(a.returncode,0)
        self.assertEqual((self.root/'state').read_text(),'running')
        self.assertEqual((self.root/'events').read_text(),'start\n')
        (self.root/'b.finish').touch();b.communicate(timeout=8)
        self.assertEqual(b.returncode,0)
        self.wait_for(lambda:(self.root/'state').read_text()=='exited')
        self.assertEqual((self.root/'events').read_text(),'start\nstop\n')

    def test_failed_command_preserves_exit_code_and_stops(self):
        result=subprocess.run(['bash',str(self.bin/'proxy-session.sh'),'sh','-c','exit 7'],env=self.env,capture_output=True,text=True)
        self.assertEqual(result.returncode,7,result.stderr)
        self.wait_for(lambda:(self.root/'state').read_text()=='exited')

    def test_signal_exit_releases_lease(self):
        p=self.launch('term');self.wait_for(lambda:(self.root/'term.ready').exists())
        os.killpg(p.pid,signal.SIGTERM);p.communicate(timeout=8)
        self.assertNotEqual(p.returncode,0)
        self.wait_for(lambda:(self.root/'state').read_text()=='exited')

    def test_killed_wrapper_is_cleaned_when_inherited_child_lease_ends(self):
        p=self.launch('kill');self.wait_for(lambda:(self.root/'kill.ready').exists())
        p.kill();p.wait(timeout=8)
        self.assertEqual((self.root/'state').read_text(),'running')
        (self.root/'kill.finish').touch()
        p.communicate(timeout=8)
        self.wait_for(lambda:(self.root/'state').read_text()=='exited')
