# Security Policy

## Reporting a Vulnerability

Please **do not** open a public GitHub issue for security vulnerabilities.

Report them privately via GitHub's Security Advisory feature:  
**[Report a vulnerability](https://github.com/iasolanki/kubewhy/security/advisories/new)**

Include:
- A description of the vulnerability
- Steps to reproduce
- Potential impact
- Any suggested fix if you have one

You'll receive a response within 72 hours. If the issue is confirmed, a patch will be released and you'll be credited in the release notes unless you prefer otherwise.

## Scope

kubewhy is a CLI tool that reads from your local kubeconfig and calls the Anthropic API. It does not expose any network services and does not store credentials. The primary attack surfaces are:

- **kubeconfig handling** — path traversal or privilege escalation via crafted kubeconfig
- **Anthropic API key** — accidental logging or exposure of `ANTHROPIC_API_KEY`
- **Manifest parsing** (preflight) — malicious YAML passed to `kubectl apply --dry-run`
