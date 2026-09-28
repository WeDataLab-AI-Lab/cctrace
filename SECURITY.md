# Security Policy

## Supported versions

Only the latest release receives security fixes. The server API and the
database schema still change between releases, so there is no maintained
backport branch.

## Reporting a vulnerability

**Do not open a public issue.**

Use GitHub's private vulnerability reporting on this repository:
**Security -> Report a vulnerability**. It creates a private advisory that
only the maintainers can see, and it is the channel we watch.

Useful in a report:

- what an attacker can reach, and what they get
- the version or commit you tested
- a reproduction — a request, a config, or a short script
- whether the deployment was the default compose setup or a modified one

You will get an acknowledgement within **10 business days**. If the report is
confirmed we will tell you the fix timeline and credit you in the advisory
unless you ask us not to.

## Scope

`cctrace` stores agent session transcripts, which are sensitive by
construction. Reports about the following are in scope and welcome:

- authentication and session handling in the dashboard and API
- access control between accounts, projects, and admin functions
- leakage of conversation content past the configured redaction and
  exclusion settings
- injection or deserialization in the OTLP receiver and the sync endpoint
- privilege escalation from the server process into its database or container

Out of scope: findings that require an attacker who already has host or
database access, and reports against a deployment you do not operate.

## Deploying safely

The compose file ships defaults for getting started, not for exposure to a
network you do not control. `JWT_SECRET` must be a long random value, the
database and dashboard ports default to loopback, and remote dashboard access
should sit behind TLS. OTLP ports 4317/4318 default to all host interfaces for
remote collection: restrict them with firewall/VPN rules and TLS termination.
Set `HTTP_BIND`, `GRPC_BIND`, and `OTEL_HTTP_BIND` in `deploy/.env` to control
host publish addresses. The installation guide covers configuration and checks.
