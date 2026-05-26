# Security Policy

## Supported versions

Only the latest release on the `main` branch receives security fixes.

| Version | Supported |
| ------- | --------- |
| latest  | yes       |
| older   | no        |

## Reporting a vulnerability

**Please do not open a public GitHub issue for security vulnerabilities.**

Use GitHub private vulnerability reporting to submit a report. You will receive a response within 72 hours.

Please include:

- A description of the vulnerability and its potential impact
- Steps to reproduce or proof-of-concept
- Affected versions
- Any suggested mitigations (if known)

## Security model

voicelog is designed as a single-user, self-hosted tool. Please read the Security model section in the README before deploying.

Key points:

- The MCP token is the only credential protecting external access. Treat it like a password.
- The mcp container must bind to 127.0.0.1 only. Never expose port 8081 directly to the internet.
- HTTPS termination is your responsibility (nginx + certbot recommended).
- ALLOWED_USER_ID restricts the Telegram bot to a single numeric user ID.
