# Security Policy

## Supported versions

The actively maintained version is the code on the `main` branch. Older commits and prototype deployments may not
receive security fixes.

## Reporting a vulnerability

Please do not publish suspected vulnerabilities, exploit details, credentials, or personal data in a public issue.
Report them privately to the repository maintainers through GitHub's **Report a vulnerability** action / private
security advisory for this repository, if available. If private reporting is unavailable, contact the maintainers
directly and ask for a private channel before sharing technical details.

Include, when possible:

- affected commit, deployment, endpoint, or component;
- impact and conditions needed to reproduce it;
- minimal reproduction steps or a proof of concept that does not access other users' data;
- any suggested mitigation.

The maintainers will acknowledge reports as soon as practical, investigate and coordinate a fix, and agree with the
reporter on disclosure timing. Please allow time for remediation before any public disclosure. Do not run tests that
could disrupt services, access data that is not yours, or affect other users.

## Scope and security notes

This repository is a prototype. The local demo accounts and `PAYMENT_GATEWAY=fake` are for development/demo only.
Do not use demo credentials or local development secrets in a public deployment. Production deployments must set
strong secrets, HTTPS, restrictive CORS origins, secure cookie settings, and private storage credentials through the
deployment secret manager. See `docs/PLAN.md` and `deploy/k8s/README.md` for deployment prerequisites.

Security reports are welcome for the application and its deployment configuration. Availability attacks, social
engineering, and findings in third-party services outside the maintainers' control are out of scope; report those to
the relevant provider.
