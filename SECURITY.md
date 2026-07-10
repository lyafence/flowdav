# Security Policy

## Reporting a Vulnerability

Please report security vulnerabilities by emailing **spbve1fu6@mozmail.com**.

Do not open public GitHub issues for security vulnerabilities.

## Supported Versions

| Version | Supported          |
|---------|--------------------|
| latest  | :white_check_mark: |
| < latest| :x:                |

## Scope

This project encrypts traffic via AES-256-GCM + HMAC-SHA256 over
WebDAV storage. Keys are configurable and must be kept secret.
While all data is encrypted in transit and at rest on the storage
backend, the security model assumes the WebDAV provider is
untrusted. Operational security depends on key rotation and
not reusing keys across deployments.
