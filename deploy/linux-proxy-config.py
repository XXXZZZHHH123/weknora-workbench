"""Build a private, loopback-only config; import subscription nodes, not settings."""
import json
import os
from pathlib import Path
import secrets
import sys
from urllib.parse import urlsplit


def build_config(subscription):
    if not subscription:
        raise ValueError('No subscription input received')
    url = urlsplit(subscription)
    if url.scheme != 'https' or not url.hostname or any(c in subscription for c in '\r\n'):
        raise ValueError('Subscription must be a single HTTPS URL')
    return {
        'mixed-port': 17891, 'port': 0, 'socks-port': 0,
        'redir-port': 0, 'tproxy-port': 0,
        'allow-lan': False, 'bind-address': '127.0.0.1',
        'mode': 'rule', 'log-level': 'warning', 'ipv6': False,
        'find-process-mode': 'off', 'geo-auto-update': False,
        'external-controller': '127.0.0.1:17991', 'secret': secrets.token_hex(32),
        'tun': {'enable': False, 'auto-route': False, 'auto-redirect': False, 'dns-hijack': []},
        'dns': {'enable': False}, 'sniffer': {'enable': False},
        'proxy-providers': {'subscription': {
            'type': 'http', 'url': subscription, 'path': './providers/subscription.yaml',
            'interval': 86400, 'size-limit': 10485760,
            'health-check': {'enable': True, 'url': 'https://www.gstatic.com/generate_204', 'interval': 600},
        }},
        'profile': {'store-selected': True},
        'proxy-groups': [
            {'name': 'AUTO', 'type': 'url-test', 'use': ['subscription'],
             'url': 'https://www.gstatic.com/generate_204', 'interval': 600},
            {'name': 'PROXY', 'type': 'select', 'proxies': ['AUTO'], 'use': ['subscription']},
        ],
        'rules': ['IP-CIDR,127.0.0.0/8,DIRECT,no-resolve',
                  'IP-CIDR,10.0.0.0/8,DIRECT,no-resolve',
                  'IP-CIDR,172.16.0.0/12,DIRECT,no-resolve',
                  'IP-CIDR,192.168.0.0/16,DIRECT,no-resolve', 'MATCH,PROXY'],
    }


def main():
    os.umask(0o077)
    root = Path(sys.argv[1])
    config = build_config(sys.stdin.read().strip())
    path = root/'config'/'config.json'
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.exists():
        (path.parent/'config.before.json').write_bytes(path.read_bytes())
    temp = path.with_suffix('.tmp')
    temp.write_text(json.dumps(config, ensure_ascii=False, indent=2))
    temp.chmod(0o600)
    temp.replace(path)
    control = root/'config'/'control.curl'
    control.write_text('header = "Authorization: Bearer '+config['secret']+'"\n')
    control.chmod(0o600)
    print('Private subscription saved; transparent proxying, TUN and DNS listeners disabled.')


if __name__ == '__main__':
    try:
        main()
    except ValueError as exc:
        raise SystemExit(str(exc)) from None
