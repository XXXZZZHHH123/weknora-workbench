"""Syntax-only scoped routes. Authorization is always checked separately."""
from uuid import UUID


def parse_workbench_path(path: str) -> tuple[str, str] | None:
    if not isinstance(path, str):
        raise ValueError('Invalid workbench route')
    if not path.startswith('/workbenches'):
        return None
    if (not path.startswith('/workbenches/') or any(c in path for c in '%\\?#')
            or any(ord(c) < 32 or 127 <= ord(c) <= 159 for c in path)):
        raise ValueError('Invalid workbench route')
    parts = path.split('/')
    if any(part in {'.', '..'} for part in parts) or any(not p for p in parts[1:-1]):
        raise ValueError('Invalid workbench route')
    identifier = parts[2]
    try:
        parsed = UUID(identifier)
        if str(parsed) != identifier or not parsed.int:
            raise ValueError('Noncanonical workbench ID')
    except ValueError as exc:
        raise ValueError('Invalid workbench route') from exc
    suffix = '/' + '/'.join(parts[3:]) if len(parts) > 3 else '/'
    if not (suffix in {'/', '/sn', '/api/sn'} or suffix.startswith(('/sn/', '/api/sn/'))):
        raise ValueError('Invalid workbench route')
    return identifier, suffix


def allowed_workbench_route(path: str, method: str) -> bool:
    if path == '/':
        return method in {'GET', 'HEAD'}
    return (path == '/sn' or path.startswith('/sn/') or path.startswith('/api/sn/'))


def source_preparation_required(path: str) -> bool:
    # These workflows schedule tenant-only workers or use a tenant-only file registry.
    return (path.startswith('/api/sn/refresh') or path.startswith('/api/sn/import/')
            or path == '/api/sn/strategies/generate')
