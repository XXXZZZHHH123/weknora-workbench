"""Plan image reuse from tracked build inputs; never execute release.env."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tarfile

SERVICES = ('app', 'frontend', 'structured-data', 'docreader', 'postgres', 'redis')
PATHS = {
    'app': ['WeKnora'],
    'frontend': ['WeKnora/frontend', 'deploy/Dockerfile.frontend', '.dockerignore',
                 'structured-data/deploy/structured-proxy.conf'],
    'structured-data': ['structured-data/src', 'structured-data/pyproject.toml',
                        'structured-data/Dockerfile', 'structured-data/.dockerignore'],
    'docreader': ['deploy/Dockerfile.runtime'],
    'postgres': ['deploy/Dockerfile.runtime'],
    'redis': ['deploy/Dockerfile.runtime'],
}
TAG = re.compile(r'production-[a-f0-9]{40}-[0-9]+-[0-9]+\Z')
HASH = re.compile(r'[a-f0-9]{64}\Z')


def key(service):
    return service.upper().replace('-', '_')


def fingerprint(checkout, ref, recipe):
    paths = sorted(set(PATHS[recipe['service']] + [recipe['dockerfile']]))
    files = subprocess.check_output(['git', 'ls-tree', '-r', '-z', ref, '--', *paths], cwd=checkout)
    if recipe['service'] == 'app':
        ignored = subprocess.run(['git', 'show', f'{ref}:WeKnora/.dockerignore'], cwd=checkout,
                                 capture_output=True, text=True)
        rules = [line.strip() for line in ignored.stdout.splitlines()]
        excluded = []
        for directory in ('frontend', 'WeKnora-Chrome-Extension'):
            # Only recognize explicit root exclusions. On any potentially
            # overriding negation, include the directory conservatively.
            overrides = any(line.startswith('!') and
                            (line[1:].startswith(directory) or line[1:2] in '*?[')
                            for line in rules)
            if directory + '/' in rules and not overrides:
                excluded.append(('WeKnora/' + directory + '/').encode())
        files = b'\0'.join(row for row in files.split(b'\0') if row and
                           not any(row.split(b'\t', 1)[1].startswith(prefix) for prefix in excluded)) + b'\0'
    # Build version labels intentionally describe the last image build, not a
    # later docs-only deployment. Source, dependencies, recipe and platform count.
    inputs = {name: recipe[name] for name in ('service', 'context', 'dockerfile', 'base_image')}
    inputs['platform'] = 'linux/amd64'
    return hashlib.sha256(json.dumps(inputs, sort_keys=True).encode() + b'\0' + files).hexdigest()


def release_values(archive):
    if not archive:
        return {}
    with tarfile.open(archive, 'r:gz') as bundle:
        member = bundle.getmember('release.env')
        if not member.isfile() or member.size > 32768:
            raise ValueError('Invalid release.env')
        text = bundle.extractfile(member).read().decode()
    result = {}
    for line in text.splitlines():
        name, sep, value = line.partition('=')
        if not sep or not re.fullmatch(r'[A-Z_]+', name) or name in result:
            raise ValueError('Invalid release configuration')
        result[name] = value
    return result


def image_valid(image, prefix, service):
    start = f'{prefix}-{service}:'
    return image.startswith(start) and TAG.fullmatch(image[len(start):]) is not None


def plan(checkout, repository, recipes, baseline, previous=None, force=False):
    prefix = 'ghcr.io/' + repository.lower()
    previous = previous or {}
    same_repo = baseline.get('repository', '').lower() == repository.lower()
    seed = {r['service']: r for r in baseline.get('recipes', [])}
    if sorted(r['service'] for r in recipes) != sorted(SERVICES):
        raise ValueError('Exactly six unique services are required')
    rows = []
    for recipe in recipes:
        service = recipe['service']
        digest = fingerprint(checkout, 'HEAD', recipe)
        reuse = ''
        if not force and previous.get(key(service) + '_INPUT_HASH') == digest:
            candidate = previous.get(key(service) + '_IMAGE', '')
            if image_valid(candidate, prefix, service):
                reuse = candidate
        if not force and not reuse and same_repo and TAG.fullmatch(baseline.get('release_id', '')) and service in seed:
            try:
                original = fingerprint(checkout, baseline['commit'], seed[service])
            except subprocess.CalledProcessError:
                original = None
            if original == digest:
                reuse = f"{prefix}-{service}:{baseline['release_id']}"
        rows.append(dict(recipe, input_hash=digest, reuse_image=reuse))
    return {'include': rows}


def write_release(coordinates, repository, tag, destination):
    if not TAG.fullmatch(tag):
        raise ValueError('Invalid release tag')
    prefix = 'ghcr.io/' + repository.lower()
    rows = [json.loads(p.read_text()) for p in Path(coordinates).glob('*.json')]
    if sorted(r['service'] for r in rows) != sorted(SERVICES):
        raise ValueError('Missing or duplicate image coordinates')
    lines = [f'IMAGE_PREFIX={prefix}', f'IMAGE_TAG={tag}']
    for row in sorted(rows, key=lambda r: r['service']):
        if not image_valid(row['image'], prefix, row['service']) or not HASH.fullmatch(row['input_hash']):
            raise ValueError('Invalid image coordinates')
        lines += [f"{key(row['service'])}_IMAGE={row['image']}",
                  f"{key(row['service'])}_INPUT_HASH={row['input_hash']}"]
    Path(destination).write_text('\n'.join(lines) + '\n')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--repository', required=True)
    parser.add_argument('--checkout', default='.')
    parser.add_argument('--previous')
    parser.add_argument('--coordinates')
    parser.add_argument('--tag')
    parser.add_argument('--output')
    parser.add_argument('--force', action='store_true')
    args = parser.parse_args()
    if args.coordinates:
        write_release(args.coordinates, args.repository, args.tag, args.output)
    else:
        root = Path(args.checkout)
        result = plan(root, args.repository, json.loads((root/'deploy/image-builds.json').read_text()),
                      json.loads((root/'deploy/image-baseline.json').read_text()), release_values(args.previous), args.force)
        print(json.dumps(result, separators=(',', ':')))


if __name__ == '__main__':
    main()
