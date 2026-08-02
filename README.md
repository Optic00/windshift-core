<p align="center">
  <picture>
    <source media="(prefers-color-scheme: light)" srcset=".github/assets/readme-splash.svg">
    <img src=".github/assets/readme-splash-dark.svg" alt="Windshift — a self-hosted work management platform for teams" width="100%">
  </picture>
</p>

<p align="center">
  <a href="https://windshift.sh/download"><img src="https://img.shields.io/badge/download-latest-2e7dbd?style=flat-square" alt="Download"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-AGPL--3.0-2e7dbd?style=flat-square" alt="AGPL-3.0 License"></a>
  <a href="https://windshift.sh/docs"><img src="https://img.shields.io/badge/docs-windshift.sh-2e7dbd?style=flat-square" alt="Documentation"></a>
</p>

<p align="center">
  <strong>Self-hosted work management that adapts to your team.</strong><br>
  Plan projects, shape workflows, and keep delivery moving without giving up control of your data.
</p>

<p align="center">
  <img src=".github/assets/screenshots/hero-board.webp" alt="A Windshift board showing work moving from open to in progress and done" width="100%">
</p>

## One place for the work that matters

Windshift brings planning, tracking, and collaboration into a fast, flexible workspace. Start with a straightforward board, then add the structure your team needs: custom workflows, nested work items, milestones, saved searches, dashboards, and more.

It ships as a single Go binary with the Svelte frontend built in. SQLite keeps the first deployment simple, while PostgreSQL is available for teams that need it.

## Highlights

- **Plan from every angle** — use boards, backlogs, hierarchy views, milestones, iterations, and dashboards.
- **Make the workflow yours** — configure item types, statuses, fields, screens, priorities, and recurring work.
- **Keep context close** — add rich descriptions, comments, mentions, attachments, collections, and knowledge pages.
- **Collaborate beyond the team** — share public boards and accept external requests through a customer portal.
- **Connect your tools** — integrate GitHub, Gitea or Forgejo, import Jira projects, and send email or webhook notifications.
- **Go beyond issue tracking** — enable test management, time tracking and billing, or asset management when you need them.

Authentication options include local sessions, WebAuthn/FIDO2, and SSO through OIDC providers such as Pocket ID and Authentik.

For local and homelab deployments, `BASE_URL` may use `localhost`, an IP
address, or a dotted local DNS name such as `windshift.home.arpa`. WebAuthn
uses the hostname from that URL as its RP ID, without the scheme or port, so
`http://localhost:7777` uses the valid RP ID `localhost`. HTTP on `localhost`
is a WebAuthn development exception; HTTPS is required for other hostnames.

If `BASE_URL` uses a single-label name such as `windshift`, Windshift still
starts normally but disables passkey routes and logs the reason. Use a dotted
hostname, `localhost`, an IP address, or a compatible explicit
`WEBAUTHN_RP_ID` when passkeys are required.

## Get started

[Download the latest release](https://windshift.sh/download), then follow the [quick start guide](https://windshift.sh/docs/01-getting-started/02-quick-start). Windshift is designed to run comfortably on anything from a Raspberry Pi to a dedicated server.

Want to build from source? See [BUILD.md](BUILD.md). For local development and contribution guidelines, see [CONTRIBUTING.md](CONTRIBUTING.md).

## Contributing

Code contributions, bug reports, and feedback are all welcome here on GitHub. Open pull requests against `main`; use GitHub Issues and Discussions for bug reports and feedback.

We are especially interested in early bug reports and real-world feedback about OIDC providers.

## Tech stack

- **Backend:** Go
- **Frontend:** Svelte, Vite, and Tailwind CSS
- **Database:** SQLite by default, with PostgreSQL support
- **Authentication:** Sessions, JWT, WebAuthn, and OIDC

## Documentation

- [Build instructions](BUILD.md)
- [Contributing guide](CONTRIBUTING.md)
- [Logging configuration](LOGGING.md)
- [Product documentation](https://windshift.sh/docs)

## License

Windshift is available under the [GNU Affero General Public License v3.0](LICENSE).
