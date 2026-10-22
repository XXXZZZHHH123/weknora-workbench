"""MinerU 4.0.12 CPU ONNX + official local llama.cpp VLM service.

Only deployment-owned files configure credentials. They never become CLI flags,
image layers, health responses or entrypoint diagnostics. This wrapper does not
make a general vision model compatible with MinerU's document VLM protocol.
"""
from __future__ import annotations

import argparse
from importlib.metadata import version
import json
import os
from pathlib import Path
import sys
from urllib.parse import urlsplit
from urllib.request import ProxyHandler, build_opener

EXPECTED_VERSION = '4.0.12'
DEFAULT_SETTINGS = '/run/secrets/mineru.json'


def positive_env(name: str, default: int, maximum: int) -> int:
    raw = os.environ.get(name, str(default))
    if not raw.isascii() or not raw.isdecimal() or not 1 <= int(raw) <= maximum:
        raise ValueError('Invalid numeric deployment setting')
    return int(raw)


def load_settings(path: str | Path | None = None) -> dict:
    path = Path(path or os.environ.get('MINERU_SETTINGS_FILE', DEFAULT_SETTINGS))
    if not path.is_file() or path.stat().st_size > 16384:
        raise ValueError('Missing or oversized deployment settings file')
    settings = json.loads(path.read_text(encoding='utf-8'))
    allowed = {'api_key', 'vlm_mode', 'vlm_server_url', 'vlm_api_key', 'vlm_model'}
    if not isinstance(settings, dict) or set(settings) - allowed or 'api_key' not in settings:
        raise ValueError('Unexpected or missing deployment settings fields')
    if any(not isinstance(v, str) or any(ord(c) < 32 for c in v) for v in settings.values()):
        raise ValueError('Deployment connection fields must be single-line strings')
    if len(settings['api_key']) < 32 or settings['api_key'] != settings['api_key'].strip():
        raise ValueError('A service bearer key is required')
    settings.setdefault('vlm_mode', 'local')
    if settings['vlm_mode'] not in ('local', 'remote'):
        raise ValueError('VLM mode must be explicit local or remote')
    if settings['vlm_mode'] == 'local':
        if any(settings.get(k) for k in ('vlm_server_url', 'vlm_api_key', 'vlm_model')):
            raise ValueError('Local VLM mode cannot carry remote connection values')
    else:
        if not all(k in settings for k in ('vlm_server_url', 'vlm_api_key', 'vlm_model')) or not settings['vlm_model'].strip():
            raise ValueError('Remote VLM mode requires complete connection fields and explicit model')
        parts = urlsplit(settings['vlm_server_url'])
        if (parts.scheme not in ('http', 'https') or not parts.hostname or parts.username or parts.password
                or parts.query or parts.fragment or any(c.isspace() for c in settings['vlm_server_url'])):
            raise ValueError('Invalid remote VLM service URL')
        _ = parts.port  # Reject malformed/out-of-range ports without disclosing URL.
    settings.update(
        parse_concurrency=positive_env('MINERU_PARSE_CONCURRENCY', 1, 2),
        vlm_concurrency=positive_env('MINERU_VLM_CONCURRENCY', 2, 8),
        vlm_http_timeout=positive_env('MINERU_VLM_HTTP_TIMEOUT', 600, 1800),
        retention_seconds=positive_env('MINERU_RETENTION_SECONDS', 86400, 604800),
    )
    return settings


def create_application(settings: dict):
    if version('mineru') != EXPECTED_VERSION:
        raise ValueError('Unexpected MinerU package version')
    # Fix the local backend and prevent helper LLMs from silently merging table
    # fragments or leveling headings with an independently configured provider.
    os.environ['MINERU_MODEL_SMALL_BACKEND'] = 'onnx'
    os.environ['MINERU_MODEL_SOURCE'] = 'local'
    os.environ['MINERU_LLM_AIDED_FEATURES_CROSS_PAGE_TABLE_CELL_MERGE'] = 'false'
    os.environ['MINERU_LLM_AIDED_FEATURES_TITLE_LEVELING'] = 'false'
    os.environ['MINERU_API_ENABLE_FASTAPI_DOCS'] = 'false'
    from mineru.config import VlmConfig, config
    from mineru.parser.api_server import create_app
    if config.model.small_backend != 'onnx' or config.model.source != 'local':
        raise ValueError('Unexpected local model backend configuration')
    if config.llm_aided.features.cross_page_table_cell_merge or config.llm_aided.features.title_leveling:
        raise ValueError('Unreviewed helper LLM features must be disabled')
    if settings['vlm_mode'] == 'local':
        if version('mineru-llama-cpp') != '0.1.4':
            raise ValueError('Unexpected llama.cpp runtime package version')
        vlm = VlmConfig(engine='llama-cpp', server_url='', api_key='', model='',
                        max_concurrency=settings['vlm_concurrency'])
    else:
        vlm = VlmConfig(
            engine='auto', server_url=settings['vlm_server_url'], api_key=settings['vlm_api_key'],
            model=settings['vlm_model'], max_concurrency=settings['vlm_concurrency'],
            http_timeout=settings['vlm_http_timeout'],
        )
    vlm.validate_environment()
    return create_app(
        upload_dir='/var/lib/mineru/data/uploads', tier='standard', no_flash=True,
        no_advanced=True, concurrency=settings['parse_concurrency'],
        api_key=settings['api_key'], vlm_config=vlm, preload_models=True,
        allow_local_source=False, allow_http_source=False,
        max_inline_bytes=1024 * 1024, max_url_bytes=50 * 1024 * 1024,
        retention_seconds=settings['retention_seconds'],
    )


def healthcheck() -> dict:
    opener = build_opener(ProxyHandler({}))
    def get(path: str):
        with opener.open('http://127.0.0.1:8000' + path, timeout=4) as response:
            if response.status != 200:
                raise ValueError('Unhealthy parser response')
            body = response.read(32769)
            if len(body) > 32768:
                raise ValueError('Oversized parser metadata response')
            return json.loads(body)
    health = get('/v1/health')
    tiers = get('/v1/tiers')
    if health.get('status') != 'ok' or health.get('version') != EXPECTED_VERSION:
        raise ValueError('Unexpected parser health or version')
    advertised = {entry.get('id') for entry in tiers.get('data', [])}
    if advertised != {'basic', 'standard'}:
        raise ValueError('Unexpected parser capability tiers')
    return dict(status='ok', version=EXPECTED_VERSION, tiers=sorted(advertised))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true', help='Validate mounted settings without starting or loading models')
    parser.add_argument('--healthcheck', action='store_true', help='Check local ready health, pinned version and advertised tiers')
    args = parser.parse_args()
    try:
        if args.healthcheck:
            print(json.dumps(healthcheck()))
            return 0
        settings = load_settings()
        if args.check:
            print(json.dumps(dict(settings_valid=True, expected_version=EXPECTED_VERSION,
                                  tier='standard', parse_concurrency=settings['parse_concurrency'],
                                  vlm_concurrency=settings['vlm_concurrency'], backend='onnx-cpu',
                                  vlm_mode=settings['vlm_mode'],
                                  model_quality='requires_real_parse_acceptance')))
            return 0
        application = create_application(settings)
        import uvicorn
        uvicorn.run(application, host='0.0.0.0', port=8000, log_level='warning', access_log=False)
        return 0
    except Exception as exc:
        # Validation libraries and HTTP exceptions can embed connection values.
        print('MinerU deployment check/start failed (' + type(exc).__name__ + '); check private configuration and model cache.', file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
