#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Discovery-only OIDC fixture; deliberately performs no authentication."""

import json
from http.server import BaseHTTPRequestHandler, HTTPServer

ISSUER = "http://oidc:8080"


class Discovery(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/.well-known/openid-configuration":
            self.send_error(404)
            return
        body = json.dumps({
            "issuer": ISSUER,
            "authorization_endpoint": ISSUER + "/authorize",
            "token_endpoint": ISSUER + "/token",
            "jwks_uri": ISSUER + "/jwks",
            "response_types_supported": ["code"],
            "subject_types_supported": ["public"],
            "id_token_signing_alg_values_supported": ["RS256"],
            "code_challenge_methods_supported": ["S256"],
        }).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        # Requests may contain OAuth state; never print them in CI.
        pass


if __name__ == "__main__":
    HTTPServer(("0.0.0.0", 8080), Discovery).serve_forever()
