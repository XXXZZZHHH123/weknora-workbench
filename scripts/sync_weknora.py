"""Prepare reviewed upstream updates for a vendored WeKnora snapshot.

Only a new branch is written. The main worktree and local secrets/data are not
checked out or staged. Upstream history is kept in a temporary bare repository;
only needed source blobs are copied, not its commit history.
"""
import argparse
import base64
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen
from urllib.error import HTTPError

STATE = '.upstream/weknora.json'
SHA = re.compile(r'[a-f0-9]{40}\Z')
TAG = re.compile(r'[A-Za-z0-9][A-Za-z0-9._-]{0,99}\Z')
PREFIX = 'codex/upstream-weknora-'


def git(repo, *args, data=None, check=True, env=None):
    return subprocess.run(['git', *args], cwd=repo, input=data, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE, check=check, env=env)


class GitHub:
    def __init__(self, token=None):
        self.token = token or os.environ.get('GH_TOKEN') or os.environ.get('GITHUB_TOKEN')

    def request(self, path, method='GET', payload=None):
        headers = {'Accept': 'application/vnd.github+json', 'User-Agent': 'weknora-workbench-upstream-sync'}
        if self.token:
            headers['Authorization'] = 'Bearer ' + self.token
        data = None if payload is None else json.dumps(payload).encode()
        if data is not None:
            headers['Content-Type'] = 'application/json'
        with urlopen(Request('https://api.github.com/' + path, headers=headers, data=data, method=method), timeout=60) as response:
            body = response.read()
        return json.loads(body) if body else None


def load_state(root):
    state = json.loads((Path(root) / STATE).read_text())
    if (state['repository'] != 'Tencent/WeKnora' or state['directory'] != 'WeKnora'
            or not SHA.fullmatch(state['base_commit'])
            or state['exclude'] != ['website', 'website-docs']):
        raise ValueError('Unexpected upstream configuration')
    return state


def latest_update(api, state):
    try:
        release = api.request(f"repos/{state['repository']}/releases/latest")
    except HTTPError as error:
        if error.code == 404:
            return {'status': 'no-release'}
        raise
    tag = release['tag_name']
    if release['draft'] or release['prerelease'] or not TAG.fullmatch(tag):
        raise ValueError('Latest release must be a formal release with a safe tag')
    commit = api.request(f"repos/{state['repository']}/commits/{quote(tag, safe='')}")['sha']
    if not SHA.fullmatch(commit):
        raise ValueError('Invalid upstream commit')
    report = {'tag': tag, 'target_commit': commit,
              'release_url': f"https://github.com/{state['repository']}/releases/tag/{quote(tag, safe='')}",
              'base_commit': state['base_commit']}
    if commit == state['base_commit']:
        return dict(report, status='current')
    comparison = api.request(f"repos/{state['repository']}/compare/{state['base_commit']}...{commit}")
    # A development snapshot must not be replaced with an older release or a
    # different release line merely because GitHub marks it as Latest.
    if comparison['status'] != 'ahead':
        return dict(report, status='behind' if comparison['status'] in ('behind', 'identical') else 'diverged')
    return dict(report, status='update', branch=PREFIX + tag + '-' + commit[:12])


def entries(repo, ref, recursive=False):
    args = ['ls-tree', '-z']
    if recursive:
        args.append('-r')
    result = []
    for row in git(repo, *args, ref).stdout.split(b'\0'):
        if not row:
            continue
        header, path = row.split(b'\t', 1)
        mode, kind, oid = header.decode().split()
        result.append((mode, kind, oid, path.decode()))
    return result


def tree(repo, rows):
    payload = b''.join(f'{mode} {kind} {oid}\t'.encode() + name.encode() + b'\0'
                       for mode, kind, oid, name in sorted(rows, key=lambda row: row[3]))
    return git(repo, 'mktree', '-z', data=payload).stdout.decode().strip()


def replace_path(repo, root_tree, path, mode, kind, oid):
    name, separator, rest = path.partition('/')
    rows = entries(repo, root_tree)
    existing = next((row for row in rows if row[3] == name), None)
    if separator:
        child = existing[2] if existing and existing[1] == 'tree' else tree(repo, [])
        oid = replace_path(repo, child, rest, mode, kind, oid)
        mode, kind = '040000', 'tree'
    rows = [row for row in rows if row[3] != name]
    rows.append((mode, kind, oid, name))
    return tree(repo, rows)


def import_snapshot(source, destination, commit, exclude):
    rows = [row for row in entries(source, commit, recursive=True)
            if not any(row[3] == path or row[3].startswith(path + '/') for path in exclude)]
    if any(row[1] != 'blob' for row in rows):
        raise ValueError('Upstream introduced a submodule; manual integration is required')
    blobs = sorted({row[2] for row in rows})
    checks = git(destination, 'cat-file', '--batch-check', data=('\n'.join(blobs) + '\n').encode()).stdout
    missing = [line.split()[0].decode() for line in checks.splitlines() if line.endswith(b' missing')]
    if missing:
        with tempfile.TemporaryFile() as pack:
            subprocess.run(['git', 'pack-objects', '--stdout'], cwd=source,
                           input=('\n'.join(missing) + '\n').encode(), stdout=pack, check=True)
            pack.seek(0)
            subprocess.run(['git', 'index-pack', '--stdin'], cwd=destination, stdin=pack,
                           stdout=subprocess.DEVNULL, check=True)
    directories = {}
    for mode, kind, oid, path in rows:
        parts = path.split('/')
        node = directories
        for part in parts[:-1]:
            node = node.setdefault(part, {})
        node[parts[-1]] = (mode, kind, oid)

    def build(node):
        children = []
        for name, value in node.items():
            children.append(('040000', 'tree', build(value), name) if isinstance(value, dict) else (*value, name))
        return tree(destination, children)

    return build(directories)


def merge_vendor(repo, baseline_tree, ours_tree, upstream_tree):
    result = git(repo, 'merge-tree', '--write-tree', '--name-only', '--no-messages', '-z',
                 '--merge-base=' + baseline_tree, ours_tree, upstream_tree, check=False)
    if result.returncode not in (0, 1):
        raise RuntimeError('Git merge-tree failed: ' + result.stderr.decode(errors='replace'))
    fields = result.stdout.split(b'\0')
    merged = fields[0].decode().strip()
    if not SHA.fullmatch(merged):
        raise ValueError('Missing merged tree')
    conflicts = [field.decode() for field in fields[1:] if field] if result.returncode else []
    return merged, conflicts


def prepare(repo, state, update):
    if git(repo, 'status', '--porcelain').stdout:
        raise ValueError('Commit or stash worktree changes before preparing an update')
    if update['status'] != 'update':
        return update
    with tempfile.TemporaryDirectory(prefix='weknora-upstream-') as temporary:
        git(temporary, 'init', '--bare', '-q')
        url = 'https://github.com/' + state['repository'] + '.git'
        for commit in (state['base_commit'], update['target_commit']):
            git(temporary, 'fetch', '--quiet', '--no-tags', '--depth=1', url, commit)
        old = import_snapshot(temporary, repo, state['base_commit'], state['exclude'])
        new = import_snapshot(temporary, repo, update['target_commit'], state['exclude'])
        ours = git(repo, 'rev-parse', 'HEAD:WeKnora').stdout.decode().strip()
        merged, conflicts = merge_vendor(repo, old, ours, new)
    state = dict(state)
    if conflicts:
        state['pending_update'] = {name: update[name] for name in ('tag', 'target_commit', 'release_url')}
        state['pending_update']['conflicts'] = conflicts
    else:
        state.update(base_commit=update['target_commit'], last_release=update['tag'], release_url=update['release_url'])
        state.pop('pending_update', None)
    root_tree = git(repo, 'rev-parse', 'HEAD^{tree}').stdout.decode().strip()
    root_tree = replace_path(repo, root_tree, 'WeKnora', '040000', 'tree', merged)
    metadata = git(repo, 'hash-object', '-w', '--stdin', data=(json.dumps(state, ensure_ascii=False, indent=2) + '\n').encode()).stdout.decode().strip()
    root_tree = replace_path(repo, root_tree, STATE, '100644', 'blob', metadata)
    env = dict(os.environ, GIT_AUTHOR_NAME='WeKnora upstream updater', GIT_AUTHOR_EMAIL='upstream-update@users.noreply.github.com',
               GIT_COMMITTER_NAME='WeKnora upstream updater', GIT_COMMITTER_EMAIL='upstream-update@users.noreply.github.com')
    commit = git(repo, 'commit-tree', root_tree, '-p', 'HEAD', data=f"Update WeKnora to {update['tag']}\n".encode(), env=env).stdout.decode().strip()
    ref = 'refs/heads/' + update['branch']
    if git(repo, 'show-ref', '--verify', '--quiet', ref, check=False).returncode == 0:
        raise ValueError('Update branch already exists; refusing to overwrite edits')
    git(repo, 'update-ref', ref, commit, '0' * 40)
    return dict(update, commit=commit, conflicts=conflicts, status='conflicts' if conflicts else 'prepared')


def create_pull_request(api, repository, report):
    conflicts = report['conflicts']
    lines = [f"将 WeKnora 从 `{report['base_commit']}` 更新到正式版 `{report['tag']}`（`{report['target_commit']}`）。",
             f"上游发布：[版本说明]({report['release_url']})。", '',
             '通过上游基线、本项目定制源码、新版上游源码进行三方合并。',
             '保留独立结构化服务、部署配置及被排除的官网目录；本 PR 不自动合并。', '']
    if conflicts:
        lines += ['存在待解决冲突；基线尚未推进，检查会阻止此 PR 发布：', '']
        lines += ['- `WeKnora/' + path.replace('`', '\\`') + '`' for path in conflicts]
        lines += ['', '在此分支解决冲突后执行 `python3 scripts/sync_weknora.py --finalize`，提交并推送，再确认检查结果。']
    else:
        lines += ['自动三方合并没有文本冲突，仍需检查业务兼容性。']
    lines += ['', '需审查数据库迁移、CentOS 7 运行兼容性、定制界面与 MCP 行为，以及 `deploy/image-builds.json` 中的依赖镜像版本。',
              'CI 使用分支 workflow_dispatch 自动执行；该分支不会发布生产版本。合并 main 后才进入现有 CI/CD。']
    return api.request(f'repos/{repository}/pulls', method='POST', payload={
        'title': f"Update WeKnora to {report['tag']}" + (' (resolve conflicts)' if conflicts else ''),
        'head': report['branch'], 'base': 'main', 'body': '\n'.join(lines), 'draft': bool(conflicts)})


def ensure_ci(api, repository, branch, commit):
    query = urlencode({'branch': branch, 'event': 'workflow_dispatch', 'per_page': 30})
    runs = api.request(f'repos/{repository}/actions/workflows/cicd.yml/runs?' + query)
    if any(run['head_sha'] == commit for run in runs['workflow_runs']):
        return False
    api.request(f'repos/{repository}/actions/workflows/cicd.yml/dispatches', method='POST', payload={'ref': branch})
    return True


def publish_update(api, repo, repository, state, update):
    if not repository or not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repository):
        raise ValueError('Invalid destination repository')
    opened = api.request(f'repos/{repository}/pulls?' + urlencode({'state': 'open', 'base': 'main', 'per_page': 100}))
    for pull in opened:
        head_repo = pull['head'].get('repo') or {}
        if pull['head']['ref'].startswith(PREFIX) and head_repo.get('full_name', '').lower() == repository.lower():
            triggered = ensure_ci(api, repository, pull['head']['ref'], pull['head']['sha'])
            return dict(update, status='existing-pr', pull_request_url=pull['html_url'], ci_dispatched=triggered)
    query = urlencode({'state': 'all', 'base': 'main', 'head': repository.split('/')[0] + ':' + update['branch'], 'per_page': 100})
    previous = api.request(f'repos/{repository}/pulls?' + query)
    if previous:
        return dict(update, status='existing-pr', pull_request_url=previous[0]['html_url'])
    try:
        existing = api.request(f"repos/{repository}/branches/{quote(update['branch'], safe='')}")
    except HTTPError as error:
        if error.code != 404:
            raise
        existing = None
    if existing:
        # Resume a run that pushed successfully but failed before creating its PR.
        contents = api.request(f'repos/{repository}/contents/{STATE}?' + urlencode({'ref': update['branch']}))
        recorded = json.loads(base64.b64decode(contents['content']))
        pending = recorded.get('pending_update')
        recorded_target = pending['target_commit'] if pending else recorded['base_commit']
        if recorded_target != update['target_commit']:
            raise ValueError('Existing branch has another target; refusing to overwrite it')
        report = dict(update, commit=existing['commit']['sha'], conflicts=pending['conflicts'] if pending else [],
                      status='conflicts' if pending else 'prepared')
    else:
        report = prepare(repo, state, update)
        # No force push: manual branch edits are never overwritten.
        git(repo, 'push', 'origin', report['commit'] + ':refs/heads/' + report['branch'])
    pull = create_pull_request(api, repository, report)
    triggered = ensure_ci(api, repository, report['branch'], report['commit'])
    return dict(report, pull_request_url=pull['html_url'], ci_dispatched=triggered)


def validate_state(root):
    state = load_state(root)
    if state.get('pending_update'):
        raise ValueError('Unresolved WeKnora upstream update. Resolve conflicts and run --finalize on the update branch.')


def finalize(root):
    state = load_state(root)
    pending = state.get('pending_update')
    branch = git(root, 'symbolic-ref', '--short', 'HEAD').stdout.decode().strip()
    if not pending or not branch.startswith(PREFIX):
        raise ValueError('Finalize is only available on an update branch with pending conflicts')
    for path in pending['conflicts']:
        full = Path(root) / 'WeKnora' / path
        if full.exists() and re.search(rb'(?m)^(<<<<<<< |=======$|>>>>>>> )', full.read_bytes()):
            raise ValueError('Conflict markers remain in WeKnora/' + path)
    state.update(base_commit=pending['target_commit'], last_release=pending['tag'], release_url=pending['release_url'])
    state.pop('pending_update')
    (Path(root) / STATE).write_text(json.dumps(state, ensure_ascii=False, indent=2) + '\n')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--prepare', action='store_true')
    parser.add_argument('--create-pr', action='store_true')
    parser.add_argument('--validate-state', action='store_true')
    parser.add_argument('--finalize', action='store_true')
    parser.add_argument('--repository', default=os.environ.get('GITHUB_REPOSITORY'))
    parser.add_argument('--output')
    args = parser.parse_args()
    root = Path(git('.', 'rev-parse', '--show-toplevel').stdout.decode().strip())
    if args.validate_state:
        validate_state(root)
        return
    if args.finalize:
        finalize(root)
        print('Metadata finalized. Commit resolved files and metadata on this branch, then push for CI.')
        return
    state = load_state(root)
    validate_state(root)
    api = GitHub()
    report = latest_update(api, state)
    if report['status'] == 'update':
        if args.create_pr:
            report = publish_update(api, root, args.repository, state, report)
        elif args.prepare:
            report = prepare(root, state, report)
    text = json.dumps(report, ensure_ascii=False, indent=2)
    if args.output:
        Path(args.output).write_text(text + '\n')
    print(text)


if __name__ == '__main__':
    main()
