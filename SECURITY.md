# Security Policy

## Supported versions

Headroom is pre-1.0, so only the latest minor release line gets security fixes.
A fix lands on `main` and ships in the next patch release of that line. Older
lines get no backports, so upgrade to the latest release instead.

| Version | Supported |
|---|---|
| `0.2.x` | ✅ |
| `0.1.x` | ❌ |
| `main` | ✅ (unreleased) |

Release candidates (`-rc.N` tags) are for validation only and are never
supported once their final release ships.

## Reporting a vulnerability

**Please do not open a public issue for security vulnerabilities.**

Report privately through GitHub's
[private vulnerability reporting](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability):

1. Go to the repository's **Security** tab.
2. Click **Report a vulnerability**.
3. Fill in as much detail as you can (see below).

If you are unable to use that flow, email the maintainer at
**karlkfi@gmail.com** with `SECURITY` in the subject line.

A useful report includes:

- The affected component (policy core, controller, admission webhook, RBAC/
  manifests) and version or commit.
- A description of the issue and its security impact.
- Steps to reproduce, a proof of concept, or a failing test if you have one.
- Any suggested remediation.

### What to expect

This is currently a solo, best-effort project, so timelines are targets rather
than guarantees:

- **Acknowledgement** within 5 business days.
- An initial assessment (severity, affected versions) within 10 business days.
- Coordinated disclosure: we will agree on a disclosure timeline with you and
  credit you in the advisory unless you prefer to remain anonymous.

Fixes are developed privately and published as a
[GitHub Security Advisory](https://docs.github.com/en/code-security/security-advisories/repository-security-advisories/about-repository-security-advisories)
once a patch is available.

## No security regression

Security posture is treated as a property to preserve, not a one-time gate.
Changes are expected **not** to regress it:

- CI runs [`govulncheck`](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck)
  against dependencies on every PR (`make govulncheck`); a newly introduced
  known vulnerability fails the build.
- [Dependabot](.github/dependabot.yml) keeps Go modules and GitHub Actions
  patched.
- The controller ships least-privilege RBAC; broaden it only with justification,
  and regenerate manifests from the source markers (never hand-edit
  `config/rbac`).

If a change must reduce an existing protection, call it out explicitly in the PR
description so the trade-off is reviewed rather than merged silently.
