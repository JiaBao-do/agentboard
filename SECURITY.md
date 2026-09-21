# Security policy

## Supported versions

The latest release receives security fixes.

## Reporting a vulnerability

Please use GitHub's private vulnerability reporting on this repository (Security tab, "Report a
vulnerability"). Do not open a public issue for security problems. You can expect an acknowledgement
within a few days.

## Threat model in brief

- agentboard binds to `127.0.0.1` by default. Binding elsewhere requires a token.
- Task text comes from agents and is treated as untrusted: the UI renders it with `textContent`, never as HTML,
  under a strict Content-Security-Policy.
- A web page you visit can make your browser call a server on localhost. State-changing requests require
  `Content-Type: application/json` (forcing a CORS preflight the server never answers); the shutdown endpoint
  additionally requires a custom header, a same-origin `Origin`, and an allowed `Host` or a token.
- The optional token is compared in constant time. Tokens in `?token=` (needed for the browser event stream) can
  appear in logs of proxies you put in front; prefer running without a proxy or strip query strings.
- There is no TLS built in. If you expose the port beyond localhost, terminate TLS in front of it.
