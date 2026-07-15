# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| latest  | :white_check_mark: |
| < latest| :x:                |

## Reporting a Vulnerability

Report vulnerabilities privately via **GitHub Security Advisories**.
Do **not** open public GitHub Issues for security vulnerabilities.

Response time: within 72 hours for initial acknowledgment.

## Scope

This project encrypts traffic via AES-256-GCM + HMAC-SHA256 over
WebDAV storage. Keys are configurable and must be kept secret.
While all data is encrypted in transit and at rest on the storage
backend, the security model assumes the WebDAV provider is
untrusted. Operational security depends on key rotation and
not reusing keys across deployments.
