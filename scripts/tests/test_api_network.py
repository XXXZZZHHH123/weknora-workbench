import contextlib
import importlib.util
import io
import json
from pathlib import Path
import socket
import ssl
import tempfile
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / "check-api-network.py"
spec = importlib.util.spec_from_file_location("api_network", SCRIPT)
network = importlib.util.module_from_spec(spec)
spec.loader.exec_module(network)


class APINetworkTests(unittest.TestCase):
    def test_endpoint_never_reports_path_query_or_credentials(self):
        self.assertEqual(network.endpoint("https://api.example.invalid/v1/secret?api_key=hidden#token"),
                         {"scheme": "https", "host": "api.example.invalid", "port": 443})
        for value in ("file:///tmp/secret", "https://user:password@example.invalid", "https://api.invalid:0"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                network.endpoint(value)

    def test_fake_dns_blocks_even_explicit_direct_connection(self):
        target = network.endpoint("https://api.example.invalid") | {"direct": True}
        with patch.object(socket, "getaddrinfo", return_value=[(None, None, None, None, ("198.18.0.7", 443))]), \
                patch.object(socket, "create_connection") as connect:
            result = network.inspect_endpoint(target, "tls", 1)
        self.assertEqual(result["dns_status"], "fake_ip")
        self.assertEqual(result["probe_status"], "blocked_address")
        connect.assert_not_called()

    def test_restricted_dns_requires_explicit_internal_endpoint(self):
        target = network.endpoint("http://parser.invalid:50051")
        with patch.object(socket, "getaddrinfo", return_value=[(None, None, None, None, ("10.21.0.7", 50051))]), \
                patch.object(socket, "create_connection") as connect:
            denied = network.inspect_endpoint(target, "tcp", 1)
            connect.assert_not_called()
            allowed = network.inspect_endpoint(target | {"direct": True}, "tcp", 1)
        self.assertEqual(denied["probe_status"], "blocked_address")
        self.assertEqual(allowed["probe_status"], "tcp_ok")

    def test_ipv6_metadata_is_never_a_trusted_direct_probe(self):
        target = network.endpoint("https://metadata.invalid") | {"direct": True}
        with patch.object(socket, "getaddrinfo", return_value=[(None, None, None, None, ("fd00:ec2::254", 443))]), \
                patch.object(socket, "create_connection") as connect:
            result = network.inspect_endpoint(target, "tls", 1)
        self.assertEqual(result["probe_status"], "blocked_address")
        connect.assert_not_called()
        with self.assertRaises(ValueError):
            network.mihomo_fragment([network.endpoint("http://[fd00:ec2::254]") | {"direct": True}], "blacklist")

    def test_tls_verifies_hostname_and_does_not_write_http(self):
        target = network.endpoint("https://api.example.invalid")
        with patch.object(socket, "getaddrinfo", return_value=[(None, None, None, None, ("1.1.1.1", 443))]), \
                patch.object(socket, "create_connection") as connect, \
                patch.object(ssl, "create_default_context") as secure:
            result = network.inspect_endpoint(target, "tls", 1)
        self.assertEqual(result["probe_status"], "tls_ok")
        secure.return_value.wrap_socket.assert_called_once_with(
            connect.return_value.__enter__.return_value, server_hostname="api.example.invalid")
        connect.return_value.send.assert_not_called()
        connect.return_value.sendall.assert_not_called()

    def test_tls_certificate_failure_remains_a_failure(self):
        with patch.object(socket, "getaddrinfo", return_value=[(None, None, None, None, ("1.1.1.1", 443))]), \
                patch.object(socket, "create_connection"), \
                patch.object(ssl, "create_default_context") as secure:
            secure.return_value.wrap_socket.side_effect = ssl.SSLCertVerificationError("fixture")
            result = network.inspect_endpoint(network.endpoint("https://api.invalid"), "tls", 1)
        self.assertEqual(result["probe_status"], "failed")
        self.assertEqual(result["errors"], ["certificate_error"])

    def test_dns_error_does_not_copy_resolver_error_secrets(self):
        with patch.object(socket, "getaddrinfo", side_effect=socket.gaierror("secret error text")):
            result = network.inspect_endpoint(network.endpoint("https://api.invalid"), "dns", 1)
        self.assertEqual(result["dns_status"], "error")
        self.assertNotIn("secret", json.dumps(result))

    def test_empty_dns_answer_is_an_error(self):
        with patch.object(socket, "getaddrinfo", return_value=[]):
            result = network.inspect_endpoint(network.endpoint("https://api.invalid"), "dns", 1)
        self.assertEqual(result["dns_status"], "error")

    def test_filter_modes_keep_opposite_whitelist_semantics_explicit(self):
        targets = [network.endpoint("https://api.invalid")]
        blacklist = network.mihomo_fragment(targets, "blacklist")
        self.assertEqual(blacklist["dns"]["fake-ip-filter"], ["api.invalid"])
        rules = network.mihomo_fragment(targets, "rule")
        self.assertEqual(rules["dns"]["fake-ip-filter"], ["DOMAIN,api.invalid,real-ip"])
        whitelist = network.mihomo_fragment(targets, "whitelist")
        self.assertIsNone(whitelist["dns"])
        self.assertIn("Do not append", " ".join(whitelist["notes"]))
        self.assertEqual(whitelist["rules"], [])

    def test_only_explicit_direct_targets_generate_exact_routes(self):
        targets = [network.endpoint("https://api.invalid"),
                   network.endpoint("http://10.21.0.7:18090") | {"direct": True},
                   network.endpoint("http://parser.invalid") | {"direct": True}]
        fragment = network.mihomo_fragment(targets, "blacklist")
        self.assertEqual(fragment["rules"], ["DOMAIN,parser.invalid,DIRECT", "IP-CIDR,10.21.0.7/32,DIRECT,no-resolve"])
        self.assertEqual(fragment["tun"]["route-exclude-address"], ["10.21.0.7/32"])
        for forbidden in ("198.18.0.7", "169.254.169.254", "0.0.0.0"):
            with self.subTest(address=forbidden), self.assertRaises(ValueError):
                network.mihomo_fragment([network.endpoint(f"http://{forbidden}") | {"direct": True}], "blacklist")

    def test_json_config_cli_prints_only_origins_and_no_proxy_secrets(self):
        with tempfile.TemporaryDirectory() as temporary:
            config = Path(temporary) / "endpoints.json"
            config.write_text(json.dumps({"models": [{"parameters": {
                "base_url": "https://api.invalid/private?api_key=url-secret", "api_key": "config-secret"}}]}))
            output = io.StringIO()
            with patch.object(socket, "getaddrinfo", return_value=[(None, None, None, None, ("1.1.1.1", 443))]), \
                    patch.dict(network.os.environ, {"HTTPS_PROXY": "http://user:proxy-secret@proxy.invalid:8080"}), \
                    contextlib.redirect_stdout(output):
                status = network.run(["--config", str(config), "--filter-mode", "blacklist"])
            report = json.loads(output.getvalue())
        self.assertEqual(status, 0)
        self.assertEqual(report["api_requests_sent"], 0)
        self.assertTrue(report["environment_proxy_present"]["HTTPS_PROXY"])
        for hidden in ("url-secret", "config-secret", "proxy-secret", "private", "proxy.invalid"):
            self.assertNotIn(hidden, output.getvalue())


if __name__ == "__main__":
    unittest.main()
