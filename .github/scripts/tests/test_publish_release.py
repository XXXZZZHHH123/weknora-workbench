import argparse
from copy import deepcopy
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import urllib.error


SCRIPT = Path(__file__).resolve().parents[1] / "publish_release.py"
spec = importlib.util.spec_from_file_location("publish_release", SCRIPT)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
SHA = "a" * 40
NEXT = "b" * 40


def arguments():
    return argparse.Namespace(repository="owner/workbench", commit=SHA, run_id=42,
                              run_attempt=1, bundle="deployment.tar.gz",
                              checksum="deployment.tar.gz.sha256", notes_file="notes.txt")


class FakeGitHub:
    def __init__(self, heads):
        self.heads = iter(heads)
        self.writes = []
        self.state = None
        self.latest_release = {"id": 5}
        self.create_error = None
        self.publish_error = None
        self.retain_error = None
        self.after_publish = None
        self.after_retain = None
        self.create_partial = False
        self.tag_override = None
        self.publish_applied = True
        self.uploads = []
        self.read_ids = []

    def head(self):
        value = next(self.heads)
        if isinstance(value, Exception):
            raise value
        return value

    def release(self, tag, release_id=None):
        self.read_ids.append(release_id)
        return deepcopy(self.state)

    def latest(self):
        return deepcopy(self.latest_release)

    def tag_target(self, tag):
        if self.tag_override:
            return self.tag_override
        return SHA if self.state and not self.state["draft"] else None

    def create_draft(self, tag, args):
        self.writes.append(("create", tag))
        if not self.create_error or self.create_partial:
            self.state = {"id": 9, "tag_name": tag, "target_commitish": SHA,
                          "draft": True, "prerelease": False, "assets": [],
                          "html_url": "https://example.invalid/9"}
        if self.create_error:
            raise self.create_error
        return deepcopy(self.state)

    def upload_assets(self, release, args):
        self.uploads.append(release["id"])
        self.state["assets"] = [{"name": Path(name).name, "state": "uploaded", "size": 10}
                                for name in (args.bundle, args.checksum)]

    def publish(self, release_id, tag, commit):
        assert (release_id, tag, commit) == (self.state["id"], self.state["tag_name"], SHA)
        self.writes.append(("publish", tag))
        if self.publish_applied:
            self.state.update(draft=False, published_at="2026-01-01T00:00:00Z")
            self.latest_release = deepcopy(self.state)
        if self.after_publish:
            self.after_publish(self)
        if self.publish_error:
            raise self.publish_error

    def retain_draft(self, release_id, tag, commit):
        assert (release_id, tag, commit) == (self.state["id"], self.state["tag_name"], SHA)
        self.writes.append(("retain", tag))
        if self.retain_error:
            raise self.retain_error
        self.state["draft"] = True
        if self.latest_release.get("id") == release_id:
            self.latest_release = {"id": 5}
        if self.after_retain:
            self.after_retain(self)


class PublicationStateTests(unittest.TestCase):
    def run_with(self, client):
        return module.Publisher(client, arguments()).run()

    def operations(self, client):
        return [name for name, tag in client.writes]

    def test_superseded_before_create_keeps_all_release_state_untouched(self):
        client = FakeGitHub([NEXT])
        result = self.run_with(client)
        self.assertEqual(result["status"], "superseded")
        self.assertFalse(result["draft_retained"])
        self.assertEqual(client.writes, [])
        self.assertEqual(client.uploads, [])

    def test_current_main_publishes_and_verifies_its_exact_release(self):
        client = FakeGitHub([SHA, SHA, SHA])
        result = self.run_with(client)
        self.assertEqual(result["status"], "published")
        self.assertEqual(result["release_id"], 9)
        self.assertEqual(result["release_tag"], "production-" + SHA + "-42-1")
        self.assertEqual(self.operations(client), ["create", "publish"])
        self.assertEqual(client.read_ids[0], None)
        self.assertTrue(all(value == 9 for value in client.read_ids[1:]))

    def test_create_403_race_rechecks_head_and_reports_superseded(self):
        client = FakeGitHub([SHA, NEXT])
        client.create_error = module.PublishError("HTTP 403: Resource not accessible by integration")
        result = self.run_with(client)
        self.assertEqual(result["status"], "superseded")
        self.assertIn("HTTP 403", result["operation_error"])
        self.assertEqual(self.operations(client), ["create"])

    def test_current_head_create_permission_and_network_errors_remain_failed(self):
        for message in ("HTTP 403", "network timeout"):
            with self.subTest(message=message):
                client = FakeGitHub([SHA, SHA])
                client.create_error = module.PublishError(message)
                with self.assertRaisesRegex(module.PublishError, message):
                    self.run_with(client)
                self.assertEqual(self.operations(client), ["create"])

    def test_cannot_prove_superseded_when_head_lookup_fails(self):
        client = FakeGitHub([SHA, module.PublishError("head lookup unavailable")])
        client.create_error = module.PublishError("HTTP 403")
        with self.assertRaisesRegex(module.PublishError, "head lookup unavailable"):
            self.run_with(client)

    def test_partial_create_with_retained_draft_is_verified_without_edit(self):
        client = FakeGitHub([SHA, NEXT])
        client.create_error = module.PublishError("asset upload timeout")
        client.create_partial = True
        result = self.run_with(client)
        self.assertTrue(result["draft_retained"])
        self.assertEqual(result["status"], "superseded")
        self.assertEqual(self.operations(client), ["create"])

    def test_advanced_main_after_draft_never_calls_publish(self):
        client = FakeGitHub([SHA, NEXT])
        result = self.run_with(client)
        self.assertEqual(result["phase"], "before_publish")
        self.assertEqual(result["status"], "superseded")
        self.assertTrue(result["draft_retained"])
        self.assertTrue(client.state["draft"])
        self.assertEqual(self.operations(client), ["create"])

    def test_advance_during_publish_retracts_only_this_release_to_verified_draft(self):
        client = FakeGitHub([SHA, SHA, NEXT])
        result = self.run_with(client)
        self.assertEqual(result["status"], "superseded")
        self.assertTrue(result["compensated"])
        self.assertTrue(client.state["draft"])
        self.assertNotEqual(client.latest()["id"], result["release_id"])
        self.assertEqual(self.operations(client), ["create", "publish", "retain"])
        self.assertEqual({tag for operation, tag in client.writes}, {result["release_tag"]})

    def test_unknown_publish_outcome_after_main_advance_is_compensated(self):
        client = FakeGitHub([SHA, SHA, NEXT])
        client.publish_error = module.PublishError("response network timeout")
        result = self.run_with(client)
        self.assertEqual(result["status"], "superseded")
        self.assertIn("network timeout", result["operation_error"])
        self.assertTrue(client.state["draft"])

    def test_compensation_does_not_reset_another_releases_latest(self):
        client = FakeGitHub([SHA, SHA, NEXT])
        client.after_publish = lambda c: setattr(c, "latest_release", {"id": 99})
        result = self.run_with(client)
        self.assertEqual(result["status"], "superseded")
        self.assertEqual(client.latest(), {"id": 99})
        self.assertEqual(self.operations(client), ["create", "publish", "retain"])

    def test_current_head_publish_error_fails_even_if_server_published_it(self):
        client = FakeGitHub([SHA, SHA, SHA])
        client.publish_error = module.PublishError("response network timeout")
        with self.assertRaisesRegex(module.PublishError, "network timeout"):
            self.run_with(client)
        self.assertFalse(client.state["draft"])
        self.assertEqual(self.operations(client), ["create", "publish"])

    def test_current_head_publish_403_without_effect_still_fails(self):
        client = FakeGitHub([SHA, SHA, SHA])
        client.publish_error = module.PublishError("HTTP 403")
        client.publish_applied = False
        with self.assertRaisesRegex(module.PublishError, "HTTP 403"):
            self.run_with(client)
        self.assertTrue(client.state["draft"])
        self.assertEqual(self.operations(client), ["create", "publish"])

    def test_superseded_publish_403_retains_existing_draft_without_unneeded_write(self):
        client = FakeGitHub([SHA, SHA, NEXT])
        client.publish_error = module.PublishError("HTTP 403")
        client.publish_applied = False
        result = self.run_with(client)
        self.assertEqual(result["status"], "superseded")
        self.assertFalse(result["compensated"])
        self.assertEqual(self.operations(client), ["create", "publish"])

    def test_existing_release_is_never_overwritten(self):
        client = FakeGitHub([SHA])
        client.state = {"id": 8}
        with self.assertRaisesRegex(module.PublishError, "already exists"):
            self.run_with(client)
        self.assertEqual(client.writes, [])

    def test_missing_deployment_asset_cannot_publish_current_main(self):
        client = FakeGitHub([SHA, SHA])
        def incomplete_upload(release, args):
            client.state["assets"] = [{"name": "deployment.tar.gz", "state": "uploaded", "size": 10}]
        client.upload_assets = incomplete_upload
        with self.assertRaisesRegex(module.PublishError, "both complete deployment assets"):
            self.run_with(client)
        self.assertEqual(self.operations(client), ["create"])

    def test_partial_draft_can_stay_unfinished_when_already_superseded(self):
        client = FakeGitHub([SHA, NEXT])
        client.upload_assets = lambda release, args: None
        result = self.run_with(client)
        self.assertEqual(result["status"], "superseded")
        self.assertTrue(result["draft_retained"])
        self.assertEqual(self.operations(client), ["create"])

    def test_wrong_id_tag_or_target_prevents_compensation(self):
        for key, value in (("id", 10), ("tag_name", "other-tag"), ("target_commitish", NEXT)):
            with self.subTest(key=key):
                client = FakeGitHub([SHA, SHA, NEXT])
                client.after_publish = lambda c: c.state.update({key: value})
                with self.assertRaisesRegex(module.PublishError, "identity changed"):
                    self.run_with(client)
                self.assertEqual(self.operations(client), ["create", "publish"])

    def test_wrong_actual_tag_commit_prevents_any_publish(self):
        client = FakeGitHub([SHA])
        client.tag_override = NEXT
        with self.assertRaisesRegex(module.PublishError, "different commit"):
            self.run_with(client)
        self.assertEqual(self.operations(client), ["create"])

    def test_failed_compensation_is_not_a_successful_skip(self):
        client = FakeGitHub([SHA, SHA, NEXT])
        client.retain_error = module.PublishError("HTTP 403 retract refused")
        with self.assertRaisesRegex(module.PublishError, "retract refused"):
            self.run_with(client)
        self.assertFalse(client.state["draft"])

    def test_compensation_must_verify_both_draft_and_not_latest(self):
        for leave_published in (False, True):
            with self.subTest(leave_published=leave_published):
                client = FakeGitHub([SHA, SHA, NEXT])
                def unsafe_retain(c):
                    c.state["draft"] = not leave_published
                    c.latest_release = {"id": 9}
                client.after_retain = unsafe_retain
                with self.assertRaisesRegex(module.PublishError, "still published or latest"):
                    self.run_with(client)

    def test_retained_draft_cannot_be_called_safe_when_latest_lookup_fails(self):
        client = FakeGitHub([SHA, NEXT])
        client.latest = lambda: (_ for _ in ()).throw(module.PublishError("latest unavailable"))
        with self.assertRaisesRegex(module.PublishError, "latest unavailable"):
            self.run_with(client)


class TransportAndOutputTests(unittest.TestCase):
    def test_uploads_use_owned_release_url_and_exact_file_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            args = arguments()
            args.bundle = str(Path(directory) / "deployment.tar.gz")
            args.checksum = str(Path(directory) / "deployment.tar.gz.sha256")
            assets = []
            for name in (args.bundle, args.checksum):
                content = ("content-" + Path(name).name).encode()
                Path(name).write_bytes(content)
                assets.append({"name": Path(name).name, "state": "uploaded", "size": len(content),
                               "digest": "sha256:" + hashlib.sha256(content).hexdigest()})
            with patch.dict(os.environ, {"GH_TOKEN": "fixture-secret"}):
                client = module.GitHub("owner/workbench")
            release = {"id": 9, "upload_url": "https://uploads.github.com/repos/owner/workbench/releases/9/assets{?name,label}"}
            with patch.object(client, "send", side_effect=assets) as send:
                client.upload_assets(release, args)
            self.assertEqual(len(send.call_args_list), 2)
            for call, name in zip(send.call_args_list, (args.bundle, args.checksum)):
                self.assertEqual(call.args[0], "POST")
                self.assertTrue(call.args[1].startswith("https://uploads.github.com/repos/owner/workbench/releases/9/assets?name="))
                self.assertEqual(call.args[2], Path(name).read_bytes())

    def test_foreign_host_or_release_upload_url_never_receives_token_or_data(self):
        for url in ("https://evil.invalid/repos/owner/workbench/releases/9/assets",
                    "https://uploads.github.com/repos/owner/workbench/releases/10/assets",
                    "http://uploads.github.com/repos/owner/workbench/releases/9/assets"):
            with self.subTest(url=url), patch.dict(os.environ, {"GH_TOKEN": "fixture-secret"}):
                client = module.GitHub("owner/workbench")
                with patch.object(client, "send") as send:
                    with self.assertRaisesRegex(module.PublishError, "Unexpected Release upload URL"):
                        client.upload_assets({"id": 9, "upload_url": url}, arguments())
                    send.assert_not_called()

    def test_uploaded_asset_size_or_digest_mismatch_stays_failed(self):
        with tempfile.TemporaryDirectory() as directory:
            args = arguments()
            args.bundle = args.checksum = str(Path(directory) / "asset")
            Path(args.bundle).write_bytes(b"source-bytes")
            for change in ({"size": 0}, {"digest": "sha256:" + "0" * 64}):
                with self.subTest(change=change), patch.dict(os.environ, {"GH_TOKEN": "fixture-secret"}):
                    client = module.GitHub("owner/workbench")
                    asset = {"name": "asset", "state": "uploaded", "size": 12, **change}
                    with patch.object(client, "send", return_value=asset):
                        with self.assertRaisesRegex(module.PublishError, "verification failed"):
                            client.upload_assets({"id": 9, "upload_url": "https://uploads.github.com/repos/owner/workbench/releases/9/assets"}, args)

    def test_failed_cli_reports_actual_published_state_and_original_error(self):
        client = FakeGitHub([SHA, SHA, SHA])
        client.publish_error = module.PublishError("publish response network timeout")
        with tempfile.TemporaryDirectory() as directory:
            paths = [Path(directory) / name for name in ("bundle", "checksum", "notes")]
            for path in paths:
                path.write_text("fixture")
            argv = [str(SCRIPT), "--repository", "owner/workbench", "--commit", SHA,
                    "--run-id", "42", "--run-attempt", "1", "--bundle", str(paths[0]),
                    "--checksum", str(paths[1]), "--notes-file", str(paths[2])]
            with patch("sys.argv", argv), patch.object(module, "GitHub", return_value=client), patch.object(module, "record") as record:
                self.assertEqual(module.main(), 1)
            result = record.call_args.args[0]
            self.assertEqual(result["status"], "failed")
            self.assertIn("network timeout", result["operation_error"])
            self.assertFalse(result["observed_release"]["draft"])

    def test_rollback_patches_only_owned_id_with_exact_tag_target_and_false_latest(self):
        with patch.dict(os.environ, {"GH_TOKEN": "fixture-secret"}), patch.object(
                module.urllib.request, "urlopen") as transport:
            transport.return_value.__enter__.return_value = io.BytesIO(b'{"id": 9}')
            client = module.GitHub("owner/workbench")
            client.retain_draft(9, "this-run-tag", SHA)
        request = transport.call_args.args[0]
        self.assertEqual(request.get_method(), "PATCH")
        self.assertEqual(request.full_url, "https://api.github.com/repos/owner/workbench/releases/9")
        self.assertEqual(json.loads(request.data), {"tag_name": "this-run-tag", "target_commitish": SHA,
                         "draft": True, "make_latest": "false"})

    def test_network_failure_redacts_existing_token(self):
        with patch.dict(os.environ, {"GH_TOKEN": "fixture-secret"}), patch.object(
                module.urllib.request, "urlopen", side_effect=urllib.error.URLError("failure fixture-secret")):
            with self.assertRaisesRegex(module.PublishError, r"failure \[redacted\]"):
                module.GitHub("owner/workbench").head()

    def test_only_404_is_optional_missing_metadata(self):
        for code in (403, 404, 500):
            with self.subTest(code=code), patch.dict(os.environ, {"GH_TOKEN": "fixture-secret"}), patch.object(
                    module.urllib.request, "urlopen", side_effect=urllib.error.HTTPError(
                        "https://api.github.com", code, "failure", {}, None)):
                client = module.GitHub("owner/workbench")
                if code == 404:
                    self.assertIsNone(client.tag_target("tag"))
                else:
                    with self.assertRaisesRegex(module.PublishError, "HTTP " + str(code)):
                        client.tag_target("tag")

    def test_drafts_are_read_by_owned_id_or_paginated_list_never_published_tag_api(self):
        draft = {"id": 9, "tag_name": "this-run-tag", "draft": True}
        with patch.dict(os.environ, {"GH_TOKEN": "fixture-secret"}):
            client = module.GitHub("owner/workbench")
        def metadata(path, missing_ok=False):
            if path == "releases?per_page=100&page=1":
                return [{"id": index, "tag_name": "other"} for index in range(100)]
            if path == "releases?per_page=100&page=2":
                return [draft]
            if path == "releases/9":
                return draft
            self.fail("Unexpected endpoint: " + path)
        with patch.object(client, "get", side_effect=metadata):
            self.assertEqual(client.release("this-run-tag"), draft)
            self.assertEqual(client.release("this-run-tag", 9), draft)

    def test_output_explicitly_records_superseded_without_claiming_publication(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "output"
            summary = Path(directory) / "summary"
            result = {"status": "superseded", "release_tag": "example", "draft_retained": True}
            with patch.dict(os.environ, {"GITHUB_OUTPUT": str(output), "GITHUB_STEP_SUMMARY": str(summary)}), patch("sys.stdout", new=io.StringIO()):
                module.record(result)
            self.assertIn("status=superseded", output.read_text())
            self.assertIn('"draft_retained": true', summary.read_text())


if __name__ == "__main__":
    unittest.main()
