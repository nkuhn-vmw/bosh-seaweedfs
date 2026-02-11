# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| Latest  | :white_check_mark: |

## Reporting a Vulnerability

If you discover a security vulnerability in this project, please report it responsibly:

1. **Do not** create a public GitHub issue for security vulnerabilities
2. Email security concerns to the repository maintainers
3. Include a detailed description of the vulnerability and steps to reproduce
4. Allow reasonable time for the issue to be addressed before public disclosure

## Supply Chain Security

This project implements the following supply chain security measures:

- **Checksum verification**: All upstream assets are verified via SHA-256 checksums
- **Pinned dependencies**: Go modules use `go.sum` for integrity verification
- **Air-gap support**: All dependencies can be vendored for offline builds
- **Artifact attestation**: CI/CD pipeline supports build provenance attestation
- **Scoped secrets**: CI/CD secrets are scoped to specific environments (staging/production)
