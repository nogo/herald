# Security Policy

## Reporting a Vulnerability

Please report security issues privately using GitHub's
[**Report a vulnerability**](https://github.com/nogo/herald/security/advisories/new)
flow, or by emailing the maintainer. Do not open a public issue for security
reports.

Include the affected version, a description of the issue, and reproduction steps
if available. You can expect an initial response within a few days.

## Security Model

The full trust model, credential handling, file permissions, and network security
details live in [docs/security.md](docs/security.md). Key points:

- Secrets are encrypted at rest with [age](https://age-encryption.org/).
- The **IaC repo is root-equivalent trust** — review changes like you would root access.
- Docker socket access is root-equivalent on the host.
- Per-stack deploy directories under `services_dir` hold **cleartext** `.env` and
  Docker secret files (mode 0600). Exclude them from backups or protect those backups.
- Preview environments receive **no secrets**, and pull requests from forks cannot
  trigger them.

## Supported Versions

Security fixes are applied to the latest release. Keep the herald binary updated.
