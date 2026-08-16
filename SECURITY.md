# Security Policy

## Supported versions

Bedrock is a pre-1.0 Go module. The latest tagged release is the supported
version; `main` is development code, not a release. Older versions are not
guaranteed to receive backports. Upgrade to the newest release before reporting
a problem that may already be fixed.

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Use GitHub's
[private vulnerability reporting form](https://github.com/kzs0/bedrock/security/advisories/new)
instead. If that form is unavailable, contact the repository owner through the
contact information on their [GitHub profile](https://github.com/kzs0).

Include enough information to reproduce and assess the report:

- affected Bedrock version or commit;
- deployment and relevant configuration;
- impact and a minimal proof of concept;
- known workarounds; and
- whether the issue or proof of concept has been disclosed elsewhere.

There is no fixed response SLA. The maintainers will investigate reports as
capacity allows and coordinate a disclosure date when a fix is required.
Please keep details private until a release or coordinated disclosure is ready.

## Scope and deployment responsibility

This policy covers the source code in this repository. Report vulnerabilities
in Go, an operating system, collector, proxy, or other third-party service to
that project as well.

Observability data and endpoints can expose operational details. Deployers
should authenticate or isolate collectors, keep credentials out of telemetry,
bind observability endpoints only to intended interfaces, and enable profiling
only where its cost and exposure are acceptable. Review configuration when
upgrading; security-sensitive defaults and validation may become stricter.
