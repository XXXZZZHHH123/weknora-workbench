#!/usr/bin/env python3
"""Inspect endpoint DNS and optional TCP/TLS without sending an API request."""
from __future__ import annotations

import argparse
import ipaddress
import json
import os
from pathlib import Path
import re
import socket
import ssl
from urllib.parse import urlsplit


FAKE_IPV4 = ipaddress.ip_network("198.18.0.0/15")
URL_KEYS = frozenset({"base_url", "baseURL", "api_url", "endpoint", "url"})


def endpoint(raw: str) -> dict:
    try:
        parsed = urlsplit(raw)
        if parsed.scheme not in {"http", "https"} or not parsed.hostname or parsed.username is not None:
            raise ValueError
        host = parsed.hostname.encode("idna").decode("ascii").lower().rstrip(".")
        port = parsed.port
        if port is None:
            port = 443 if parsed.scheme == "https" else 80
        try:
            ipaddress.ip_address(host)
        except ValueError:
            if not re.fullmatch(r"[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?", host):
                raise ValueError
        if not 1 <= port <= 65535:
            raise ValueError
    except (ValueError, UnicodeError):
        raise ValueError("endpoint must be an HTTP(S) URL with a valid host and no user information") from None
    # Paths, query strings, headers and credentials never enter the report.
    return {"scheme": parsed.scheme, "host": host, "port": port}


def configured_urls(value):
    if isinstance(value, list):
        for item in value:
            if isinstance(item, str):
                yield item
            else:
                yield from configured_urls(item)
    elif isinstance(value, dict):
        for key, item in value.items():
            if key in URL_KEYS and isinstance(item, str) and item:
                yield item
            elif isinstance(item, (dict, list)):
                yield from configured_urls(item)


def address_class(raw: str) -> str:
    address = ipaddress.ip_address(raw)
    if address.version == 4 and address in FAKE_IPV4:
        return "fake_ip"
    return "public" if address.is_global else "restricted"


def trusted_direct_address(raw: str) -> bool:
    address = ipaddress.ip_address(raw)
    if address == ipaddress.ip_address("fd00:ec2::254"):
        return False
    private_ranges = ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7")
    return address.is_global or address.is_loopback or any(address in ipaddress.ip_network(net) for net in private_ranges)


def inspect_endpoint(target: dict, probe: str, timeout: float) -> dict:
    result = dict(target)
    try:
        addresses = sorted({row[4][0] for row in socket.getaddrinfo(
            target["host"], target["port"], type=socket.SOCK_STREAM)})
    except OSError:
        return result | {"dns_status": "error", "addresses": [], "probe_status": "not_run"}
    if not addresses:
        return result | {"dns_status": "error", "addresses": [], "probe_status": "not_run"}
    classes = {address_class(address) for address in addresses}
    status = "fake_ip" if "fake_ip" in classes else "restricted" if "restricted" in classes else "public"
    result.update(addresses=addresses, dns_status=status, probe_status="not_requested")
    if probe == "dns":
        return result
    if status == "fake_ip" or (status == "restricted" and
            (not target.get("direct") or not all(trusted_direct_address(address) for address in addresses))):
        return result | {"probe_status": "blocked_address"}
    errors = set()
    for address in addresses:
        try:
            with socket.create_connection((address, target["port"]), timeout=timeout) as connection:
                if probe == "tls" and target["scheme"] == "https":
                    with ssl.create_default_context().wrap_socket(connection, server_hostname=target["host"]):
                        pass
            return result | {"probe_status": "tls_ok" if probe == "tls" and target["scheme"] == "https" else "tcp_ok"}
        except ssl.SSLCertVerificationError:
            errors.add("certificate_error")
        except (OSError, ssl.SSLError):
            errors.add("connection_error")
    return result | {"probe_status": "failed", "errors": sorted(errors)}


def mihomo_fragment(targets: list[dict], mode: str) -> dict:
    hosts = sorted({target["host"] for target in targets if not _literal_ip(target["host"])})
    real_rules = [f"DOMAIN,{host},real-ip" for host in hosts]
    notes = ["Merge fragments into the existing profile; this tool does not change proxy, DNS or route settings."]
    if mode == "blacklist":
        dns = {"fake-ip-filter-mode": mode, "fake-ip-filter": hosts}
        notes.append("Append these exact domains to the existing blacklist; retain its existing entries.")
    elif mode == "rule":
        dns = {"fake-ip-filter-mode": mode, "fake-ip-filter": real_rules}
        notes.append("Prepend these real-ip rules before existing rules and MATCH; retain the existing fallback.")
    else:
        dns = None
        notes.append("Whitelist mode returns fake IPs for matching domains. Do not append these domains. Remove matching entries; broad matches require an explicitly reviewed conversion to rule mode.")
    routes, excluded = [], []
    for target in targets:
        if not target.get("direct"):
            continue
        host = target["host"]
        address = _literal_ip(host)
        if address is None:
            routes.append(f"DOMAIN,{host},DIRECT")
        else:
            if not trusted_direct_address(str(address)):
                raise ValueError("DIRECT targets must use a public, loopback or private LAN address")
            cidr = f"{address}/{address.max_prefixlen}"
            routes.append(f"IP-CIDR{'6' if address.version == 6 else ''},{cidr},DIRECT,no-resolve")
            excluded.append(cidr)
    return {"dns": dns, "real_ip_rule_entries": real_rules, "rules": sorted(set(routes)),
            "tun": {"route-exclude-address": sorted(set(excluded))}, "notes": notes}


def _literal_ip(host: str):
    try:
        return ipaddress.ip_address(host)
    except ValueError:
        return None


def run(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", action="append", default=[], help="Model or parser endpoint; repeat as needed")
    parser.add_argument("--direct-url", action="append", default=[], help="Explicitly trusted platform/internal endpoint to route DIRECT")
    parser.add_argument("--config", action="append", type=Path, default=[], help="JSON configuration containing endpoint/base_url/url fields; never printed")
    parser.add_argument("--filter-mode", choices=("blacklist", "whitelist", "rule"), required=True,
                        help="Use the active Mihomo profile's fake-ip-filter-mode (default in Mihomo is blacklist)")
    parser.add_argument("--probe", choices=("dns", "tcp", "tls"), default="dns")
    parser.add_argument("--timeout", type=float, default=3)
    args = parser.parse_args(argv)
    if not 0 < args.timeout <= 30:
        parser.error("timeout must be between 0 and 30 seconds")
    targets = {}
    try:
        urls = list(args.url)
        for path in args.config:
            urls.extend(configured_urls(json.loads(path.read_text(encoding="utf-8"))))
        for raw, direct in [(raw, False) for raw in urls] + [(raw, True) for raw in args.direct_url]:
            target = endpoint(raw)
            if direct and _literal_ip(target["host"]) and not trusted_direct_address(target["host"]):
                raise ValueError("unsafe DIRECT address")
            key = (target["scheme"], target["host"], target["port"])
            previous = targets.get(key, {})
            targets[key] = target | {"direct": direct or previous.get("direct", False)}
    except (ValueError, OSError):
        parser.error("Could not read endpoint configuration: require valid JSON and HTTP(S) URLs without user information")
    if not targets:
        parser.error("provide --url, --direct-url or --config")
    targets = list(targets.values())
    results = [inspect_endpoint(target, args.probe, args.timeout) for target in targets]
    report = {"endpoints": results,
              "environment_proxy_present": {name: bool(os.environ.get(name)) for name in
                  ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy")},
              "mihomo_fragment": mihomo_fragment(targets, args.filter_mode),
              "api_requests_sent": 0,
              "notes": ["NO_PROXY affects explicit HTTP proxies, not transparent TUN routing.",
                        "TCP/TLS probes connect directly; they do not use HTTP_PROXY and never disable TLS certificate checks.",
                        "A public DNS result cannot prove TUN is disabled or an authenticated API request will succeed."]}
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return int(any(row["dns_status"] in {"error", "fake_ip"} or row["probe_status"] in {"failed", "blocked_address"}
                   or row["dns_status"] == "restricted" and not row.get("direct") for row in results))


if __name__ == "__main__":
    raise SystemExit(run())
