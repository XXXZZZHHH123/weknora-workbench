"""Publish only the current main commit; retain superseded drafts and CI evidence.

GitHub has no conditional Release update tied to a branch SHA. Recheck around
each write and, if main advances during publication, retract only this run's
new release to a draft. This bounds the race; it is not an atomic guarantee.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import urllib.error
import urllib.parse
import urllib.request


class PublishError(RuntimeError):
    pass


class GitHub:
    def __init__(self, repository):
        self.repository = repository
        self.token = os.environ.get("GH_TOKEN", "")
        if not self.token:
            raise PublishError("GH_TOKEN is required")
        self.base = os.environ.get("GITHUB_API_URL", "https://api.github.com").rstrip("/")
        if not self.base.startswith("https://"):
            raise PublishError("GITHUB_API_URL must use HTTPS")

    def request(self, method, path, body=None, missing_ok=False):
        return self.send(method, f"{self.base}/repos/{self.repository}/{path}",
                         json.dumps(body).encode() if body is not None else None,
                         "application/json", missing_ok=missing_ok)

    def send(self, method, url, data, content_type, missing_ok=False):
        request = urllib.request.Request(
            url, data=data, method=method,
            headers={"Authorization": "Bearer " + self.token,
                     "Accept": "application/vnd.github+json",
                     "Content-Type": content_type,
                     "X-GitHub-Api-Version": "2022-11-28"},
        )
        try:
            with urllib.request.urlopen(request, timeout=60) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            if missing_ok and error.code == 404:
                return None
            reason = str(error.reason).replace(self.token, "[redacted]")
            try:
                payload = json.loads(error.read(8192) or b"{}")
                if isinstance(payload, dict) and payload.get("message"):
                    reason = str(payload["message"]).replace(self.token, "[redacted]")
            except (OSError, ValueError, TypeError):
                pass
            raise PublishError(f"{method}: HTTP {error.code} {reason}") from None
        except (urllib.error.URLError, OSError, ValueError) as error:
            detail = str(error).replace(self.token, "[redacted]")
            raise PublishError(f"{method}: {detail}") from None

    def get(self, path, missing_ok=False):
        return self.request("GET", path, missing_ok=missing_ok)

    def head(self):
        value = self.get("git/ref/heads/main")
        sha = value.get("object", {}).get("sha", "")
        if value.get("object", {}).get("type") != "commit" or not re.fullmatch(r"[a-f0-9]{40}", sha):
            raise PublishError("main ref is not a complete commit SHA")
        return sha

    def release(self, tag, release_id=None):
        if release_id is not None:
            return self.get(f"releases/{release_id}")
        # Tag lookup only guarantees published releases. List with this job's
        # write credential to also detect an existing draft without overwriting.
        matches = []
        for page in range(1, 101):
            values = self.get(f"releases?per_page=100&page={page}")
            matches.extend(value for value in values if value.get("tag_name") == tag)
            if len(values) < 100:
                break
        else:
            raise PublishError("Release lookup exceeded pagination budget")
        if len(matches) > 1:
            raise PublishError("Multiple Releases match this run's tag")
        return matches[0] if matches else None

    def latest(self):
        return self.get("releases/latest", missing_ok=True)

    def tag_target(self, tag):
        value = self.get("git/ref/tags/" + urllib.parse.quote(tag, safe=""), missing_ok=True)
        if value is None:
            return None
        target = value["object"]
        for _ in range(4):
            if target["type"] != "tag":
                break
            target = self.get("git/tags/" + target["sha"])["object"]
        if target["type"] != "commit":
            raise PublishError("Release tag does not resolve to a commit")
        return target["sha"]

    def create_draft(self, tag, args):
        return self.request("POST", "releases",
                            {"tag_name": tag, "target_commitish": args.commit,
                             "name": tag, "body": Path(args.notes_file).read_text(),
                             "draft": True, "prerelease": False, "make_latest": "false"})

    def upload_assets(self, release, args):
        url = release["upload_url"].split("{", 1)[0]
        parts, api = urllib.parse.urlsplit(url), urllib.parse.urlsplit(self.base)
        host = "uploads.github.com" if api.hostname == "api.github.com" else api.hostname
        suffix = f"/repos/{self.repository}/releases/{release['id']}/assets"
        if (parts.scheme != "https" or parts.hostname != host or parts.port not in (None, api.port, 443)
                or parts.username or parts.password or parts.query or parts.fragment
                or not parts.path.endswith(suffix)):
            raise PublishError("Unexpected Release upload URL; refusing to send credentials")
        for filename in (args.bundle, args.checksum):
            path = Path(filename)
            data = path.read_bytes()
            if not data:
                raise PublishError("Publication asset is empty")
            asset = self.send("POST", url + "?" + urllib.parse.urlencode({"name": path.name}),
                              data, "application/octet-stream")
            if (asset.get("name") != path.name or asset.get("state") != "uploaded"
                    or asset.get("size") != len(data)
                    or (asset.get("digest") is not None
                        and asset["digest"] != "sha256:" + hashlib.sha256(data).hexdigest())):
                raise PublishError("Uploaded asset name/state/size/digest verification failed")

    def publish(self, release_id, tag, commit):
        self.request("PATCH", f"releases/{release_id}",
                     {"tag_name": tag, "target_commitish": commit,
                      "draft": False, "make_latest": "true"})

    def retain_draft(self, release_id, tag, commit):
        self.request("PATCH", f"releases/{release_id}",
                     {"tag_name": tag, "target_commitish": commit,
                      "draft": True, "make_latest": "false"})


class Publisher:
    def __init__(self, client, args):
        self.client = client
        self.args = args
        self.tag = f"production-{args.commit}-{args.run_id}-{args.run_attempt}"
        self.phase = "before_create"
        self.owned_id = None
        self.observed_head = None
        self.last_release = None
        self.last_tag_target = None
        self.operation_error = None

    def result(self, status, **extra):
        return {"status": status, "phase": self.phase, "commit": self.args.commit,
                "observed_main": self.observed_head, "release_tag": self.tag,
                "release_id": self.owned_id, "operation_error": self.operation_error, **extra}

    def current(self):
        self.observed_head = self.client.head()
        return self.observed_head == self.args.commit

    def checked_release(self, owned=True, value=None):
        if value is None:
            value = self.client.release(self.tag, self.owned_id if owned else None)
        self.last_release = value
        if value is None:
            if owned:
                raise PublishError("This run's created Release is missing")
            return None
        if (type(value.get("id")) is not int or value["id"] <= 0
                or value.get("tag_name") != self.tag
                or value.get("target_commitish") != self.args.commit
                or value.get("prerelease") is not False
                or (owned and value["id"] != self.owned_id)):
            raise PublishError("Release identity changed; refusing to modify it")
        target = self.client.tag_target(self.tag)
        self.last_tag_target = target
        if target is not None and target != self.args.commit:
            raise PublishError("Release tag points to a different commit")
        return value

    def verified_draft(self, owned=True):
        value = self.checked_release(owned=owned)
        if value is None:
            return None
        latest = self.client.latest()
        if value.get("draft") is not True or (latest and latest.get("id") == value["id"]):
            raise PublishError("Superseded Release is still published or latest")
        return value

    def superseded(self, error=None, compensate=False):
        compensated = False
        if compensate:
            value = self.checked_release()
            if value.get("draft") is not True:
                # Only a successful create in this run establishes ownership.
                # Never edit another Release or restore somebody else's latest.
                self.client.retain_draft(self.owned_id, self.tag, self.args.commit)
                compensated = True
        value = self.verified_draft(owned=self.owned_id is not None)
        return self.result("superseded", draft_retained=value is not None,
                           release_url=value.get("html_url") if value else None,
                           compensated=compensated, operation_error=str(error) if error else None)

    def run(self):
        if not self.current():
            return self.result("superseded", draft_retained=False)
        if self.client.release(self.tag) is not None:
            raise PublishError("Release already exists; refusing to overwrite it")
        self.phase = "create_draft"
        try:
            created = self.client.create_draft(self.tag, self.args)
        except PublishError as error:
            self.operation_error = str(error)
            # The commit can lose its last ref between GET and draft creation,
            # triggering GitHub's workflow-scope 403. Unknown head stays failed.
            if not self.current():
                return self.superseded(error=error)
            raise
        if not isinstance(created, dict):
            raise PublishError("Draft creation returned no Release identity")
        value = self.checked_release(owned=False, value=created)
        if value.get("draft") is not True:
            raise PublishError("Draft creation did not return a draft Release")
        # Bind ownership to the create response, never to a later tag lookup.
        self.owned_id = value["id"]
        self.phase = "upload_assets"
        try:
            self.client.upload_assets(value, self.args)
        except PublishError as error:
            self.operation_error = str(error)
            if not self.current():
                return self.superseded(error=error)
            raise
        self.phase = "before_publish"
        value = self.checked_release()
        if not self.current():
            return self.superseded()
        self.checked_assets(value)
        self.phase = "publishing"
        try:
            self.client.publish(self.owned_id, self.tag, self.args.commit)
        except PublishError as error:
            self.operation_error = str(error)
            if not self.current():
                return self.superseded(error=error, compensate=True)
            # A current-head permission/network error is never a successful skip.
            raise
        self.phase = "after_publish"
        value = self.checked_release()
        latest = self.client.latest()
        if not self.current():
            return self.superseded(compensate=True)
        self.checked_assets(value)
        if (value.get("draft") is not False or not value.get("published_at")
                or self.last_tag_target != self.args.commit
                or not latest or latest.get("id") != self.owned_id):
            raise PublishError("Published Release/tag/latest verification failed")
        return self.result("published", release_url=value.get("html_url"))

    def checked_assets(self, release):
        assets = release.get("assets", [])
        expected = {Path(self.args.bundle).name, Path(self.args.checksum).name}
        if (len(expected) != 2 or len(assets) != 2 or {asset.get("name") for asset in assets} != expected
                or any(asset.get("state") != "uploaded" or asset.get("size", 0) <= 0 for asset in assets)):
            raise PublishError("Release must retain both complete deployment assets")


def record(result):
    print(json.dumps(result, sort_keys=True))
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            output.write(f"status={result['status']}\nrelease_tag={result['release_tag']}\n")
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as summary:
            summary.write("\nRelease publication result\n\n```json\n"
                          + json.dumps(result, indent=2, sort_keys=True) + "\n```\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--run-id", type=int, required=True)
    parser.add_argument("--run-attempt", type=int, required=True)
    parser.add_argument("--bundle", required=True)
    parser.add_argument("--checksum", required=True)
    parser.add_argument("--notes-file", required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", args.repository):
        parser.error("repository must be owner/name")
    if not re.fullmatch(r"[a-f0-9]{40}", args.commit) or min(args.run_id, args.run_attempt) < 1:
        parser.error("complete commit SHA and positive run ID/attempt required")
    for path in (args.bundle, args.checksum, args.notes_file):
        if not Path(path).is_file():
            parser.error(f"Missing publication input: {path}")
    publisher = Publisher(GitHub(args.repository), args)
    try:
        result = publisher.run()
    except (PublishError, KeyError, TypeError, ValueError, OSError) as error:
        try:
            state = publisher.client.release(publisher.tag, publisher.owned_id)
        except PublishError as read_error:
            state = {"read_error": str(read_error)}
        result = publisher.result("failed", error=str(error), observed_release=state)
        record(result)
        return 1
    record(result)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
