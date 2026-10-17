#!/usr/bin/env python3
"""Exercise deployment auth contracts with real shell, nginx, MCP and Helm."""

import json
import os
from pathlib import Path
import shlex
import socket
import subprocess
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parents[1]


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def response(url):
    try:
        with urlopen(url, timeout=2) as result:
            return result.status, result.headers, result.read()
    except HTTPError as error:
        return error.code, error.headers, error.read()


def ready(url, process):
    for _ in range(100):
        if process.poll() is not None:
            raise AssertionError(f"service exited: {process.stderr.read().decode()}")
        try:
            response(url)
            return
        except (URLError, TimeoutError):
            time.sleep(0.05)
    raise AssertionError("service did not become ready")


class DeploymentTests(unittest.TestCase):
    def test_frontend_runtime_config(self):
        source = (ROOT / "frontend/docker-entrypoint-runtime-config.sh").read_text()
        with tempfile.TemporaryDirectory() as temp:
            script = source.replace("ROOT=/usr/share/nginx/html", "ROOT=" + shlex.quote(temp))
            env = {k: v for k, v in os.environ.items() if not k.startswith(("AUTH_MODE", "OIDC_", "ZITADEL_", "FORGEJO_"))}
            env.update(AUTH_MODE="oidc", OIDC_ISSUER="https://idp.test/application/o/app/", OIDC_CLIENT_ID="app", OIDC_SCOPES="openid profile email offline_access")
            subprocess.run(["sh", "-c", script], env=env, check=True, capture_output=True)
            output = subprocess.check_output(["node", "-e", "const fs=require('fs'),vm=require('vm'); const ctx={window:{}}; vm.runInNewContext(fs.readFileSync(process.argv[1],'utf8'),ctx); console.log(JSON.stringify(ctx.window.__AINSEL_CONFIG__));", str(Path(temp) / "runtime-config.js")])
            config = json.loads(output)
            self.assertEqual(config["authMode"], "oidc")
            self.assertEqual(config["oidcProjectId"], "")
            self.assertEqual(config["oidcScopes"], env["OIDC_SCOPES"])
            env.pop("OIDC_CLIENT_ID")
            for mode in ("oidc", ""):
                env["AUTH_MODE"] = mode
                result = subprocess.run(["sh", "-c", script], env=env, capture_output=True)
                self.assertNotEqual(result.returncode, 0, "partial config must fail closed")

    def test_frontend_public_proxy(self):
        class Backend(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.end_headers()
                self.wfile.write(self.path.encode())

            def log_message(self, *_args):
                pass

        backend = ThreadingHTTPServer(("127.0.0.1", 0), Backend)
        worker = threading.Thread(target=backend.serve_forever, daemon=True)
        worker.start()
        try:
            with tempfile.TemporaryDirectory() as temp:
                listen = port()
                config = (ROOT / "frontend/nginx.conf.template").read_text()
                config = config.replace("${RESOLVER_ADDRESS}", "127.0.0.1").replace("${BACKEND_HOST}", "127.0.0.1").replace("${BACKEND_PORT}", str(backend.server_port)).replace("listen 80;", f"listen {listen};").replace("/usr/share/nginx/html", temp)
                Path(temp, "index.html").write_text("SPA")
                path = Path(temp, "nginx.conf")
                path.write_text(f"daemon off; pid {temp}/nginx.pid; error_log stderr; events {{}} http {{ access_log off; {config} }}")
                process = subprocess.Popen(["nginx", "-p", temp, "-c", str(path)], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
                try:
                    root = f"http://127.0.0.1:{listen}"
                    ready(root, process)
                    self.assertEqual(response(root + "/")[2], b"SPA")
                    self.assertEqual(response(root + "/api/v1/auth/login?test=1")[2], b"/api/v1/auth/login?test=1")
                    for suffix in ("/api/internal/events", "/api/internal/user-tokens/validate", "/api/v1/../internal/events"):
                        self.assertEqual(response(root + suffix)[0], 404, suffix)
                finally:
                    process.terminate()
                    process.wait(timeout=5)
                    process.stderr.close()
        finally:
            backend.shutdown()
            backend.server_close()

    def test_mcp_discovery_and_challenge(self):
        class Provider(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                if self.path.endswith("openid-configuration"):
                    self.wfile.write(json.dumps({"jwks_uri": f"http://127.0.0.1:{self.server.server_port}/custom/keys"}).encode())
                else:
                    self.wfile.write(b'{"keys":[]}')

            def log_message(self, *_args):
                pass

        provider = ThreadingHTTPServer(("127.0.0.1", 0), Provider)
        threading.Thread(target=provider.serve_forever, daemon=True).start()
        try:
            with tempfile.TemporaryDirectory() as temp:
                binary = str(Path(temp, "mcp"))
                subprocess.run(["go", "build", "-o", binary, "./cmd/server"], cwd=ROOT / "services/mcp", check=True)
                listen = port()
                env = {**os.environ, "PORT": str(listen), "OIDC_ISSUER": f"http://127.0.0.1:{provider.server_port}/provider/", "OIDC_AUDIENCE": "app", "OIDC_PROJECT_ID": "", "OIDC_CLIENT_ID": "app", "MCP_RESOURCE_URL": "https://app.test/mcp", "OIDC_SCOPES": "openid email offline_access", "INTERNAL_VALIDATE_SECRET": ""}
                process = subprocess.Popen([binary], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
                try:
                    root = f"http://127.0.0.1:{listen}"
                    ready(root + "/health", process)
                    status, _, data = response(root + "/.well-known/oauth-protected-resource/mcp")
                    self.assertEqual(status, 200)
                    metadata = json.loads(data)
                    self.assertEqual(metadata["authorization_servers"], [env["OIDC_ISSUER"]])
                    self.assertEqual(metadata["scopes_supported"], ["openid", "email", "offline_access"])
                    status, headers, _ = response(root + "/mcp")
                    self.assertEqual(status, 401)
                    self.assertIn('resource_metadata="https://app.test/.well-known/oauth-protected-resource/mcp"', headers["WWW-Authenticate"])
                finally:
                    process.terminate()
                    process.wait(timeout=5)
                    process.stderr.close()
        finally:
            provider.shutdown()
            provider.server_close()

    def test_chart_generic_oidc_external_database(self):
        args = ["helm", "template", "chart/", "--set", "auth.mode=oidc", "--set", "auth.oidcIssuer=https://idp.test/provider/", "--set", "auth.oidcClientId=app", "--set", "mcp.enabled=true", "--set", "mcp.externalUrl=https://app.test/mcp", "--set", "postgres.enabled=false", "--set", "postgres.host=external-rw", "--set", "postgres.port=5433", "--set", "postgres.auth.existingSecret=external-db"]
        rendered = subprocess.check_output(args, cwd=ROOT, text=True)
        self.assertIn('name: OIDC_AUDIENCE\n              value: "app"', rendered)
        self.assertIn('name: PGHOST\n              value: "external-rw"', rendered)
        self.assertIn('name: PGPORT\n              value: "5433"', rendered)
        wait_postgres = rendered.split("        - name: wait-postgres\n", 1)[1].split("      containers:\n", 1)[0]
        for setting in (
            "runAsUser: 65532",
            "runAsNonRoot: true",
            "allowPrivilegeEscalation: false",
            "drop:\n                - ALL",
        ):
            self.assertIn(setting, wait_postgres)
        self.assertNotIn("runAsUser: 0", wait_postgres)
        self.assertIn('value: "openid profile email offline_access"', rendered)
        self.assertNotIn('urn:zitadel:', rendered)
        result = subprocess.run(args + ["--set", "auth.oidcClientId="], cwd=ROOT, capture_output=True)
        self.assertNotEqual(result.returncode, 0, "OIDC without an audience must fail rendering")


if __name__ == "__main__":
    unittest.main()
