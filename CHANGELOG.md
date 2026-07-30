# Changelog

All notable changes to this project are recorded here, newest first. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Repository scaffold: single Go module with `cmd/api` and `cmd/agent`, domain-oriented
  `internal/` packages, and Makefile targets for the common tasks.
- Fail-fast configuration loading. Every required variable is validated at startup and all
  problems are reported at once; there are no fallback defaults, so a misconfigured process
  refuses to start rather than booting half-working.
- API error contract. Errors carry a stable machine code plus a plain-language message
  authored in the API, which clients render verbatim; technical causes are logged and never
  serialized. Unknown errors collapse to a single generic message.
- HTTP server with request IDs, structured access logging, panic recovery, and graceful
  shutdown. Liveness at `/health` and readiness at `/ready`.
- Tenant isolation enforced by Postgres row level security on every tenant table. The
  application connects as a restricted role, and startup refuses any role that could bypass
  the policies. Scope is applied per transaction so it cannot leak across pooled
  connections. Memberships additionally allow a user to read their own rows across
  organizations, read-only, which is how listing their organizations works.
- Password hashing with argon2id, and session tokens stored only as hashes so a database
  leak cannot be replayed as a login.
- Accounts and sessions: sign up, sign in, sign out, and current account at
  `/v1/auth/signup`, `/v1/auth/login`, `/v1/auth/logout` and `/v1/auth/me`. Browsers receive
  an httpOnly cookie, while other clients receive a token to send as a bearer credential, so
  a future mobile or command line client uses the same API with no changes.
- Passwords require twelve characters rather than a mix of character classes, which is both
  harder to guess and easier to remember.
- Cross-origin requests are permitted from the configured web origin only.
- Organizations with four roles: owner, admin, member and viewer. Create and rename an
  organization, list and manage members, change roles, and remove people.
- Invitations with single-use links that expire after seven days, including a preview page
  readable before signing in so an invitee can see who invited them.
- Every organization response includes the permissions of whoever asked, so clients show or
  hide controls from that rather than reproducing the rules themselves.
- An audit record is written for organization changes, invitations and membership changes.

- Web client scaffold: SvelteKit with Svelte 5 and Tailwind, built as a single page app that
  talks only to the API, so there is no server-side layer and no second place for logic.
- Self-hosted Mona Sans and Geist Mono as variable fonts, 60KB for both families across every
  weight, with fixed-width digits so live values do not make columns twitch as they change.
- Monochrome design tokens, with colour reserved for status and used nowhere as decoration.
- One API client for the whole app. Error messages come from the API and are shown as written,
  so every screen says the same thing and a future client behaves identically.
- Dates, numbers, byte sizes and durations are formatted with the platform's own
  internationalisation support rather than hand-written helpers, so they follow the reader's
  locale.
- Client-side validation for immediate feedback on empty or malformed fields. It deliberately
  does not reproduce rules that need stored state; the API remains the authority and its
  messages replace local ones.
- Hand-built interface components: buttons, inputs, selects, labelled fields, cards, badges,
  tables, alerts, notifications, empty states and a spinner. No third-party interface kit is
  used, so every control is ours to change.
- Field messages sit beneath the field they belong to and are announced to screen readers,
  with failures announced immediately and everything else at the next natural pause.
- Sign in, sign up and sign out screens, an authenticated shell, and screens for creating an
  organization, managing people and sending invitations.
- Invitation screen readable before signing in, so an invitee can see which organization and
  role they were offered before creating an account. Following an invitation through signing
  up or signing in returns to it afterwards, so the link is never lost.
- Opening an invitation while signed in as somebody else explains the mismatch, names both
  addresses, and offers to switch accounts, rather than failing on submission.
- Controls are shown or hidden from the permissions the API reports, so a member sees roles as
  plain text where an owner sees a role picker, and neither view reproduces the rules.
- Our own pages for addresses that do not exist and for unexpected failures.
- Development harness with three stand-in servers, each running Ubuntu with systemd, an SSH
  daemon and Docker, so the setup path used locally is the same one that will run on a real
  machine. Started with `make vps-up`; see `dev/README.md`.
- End-to-end checks that drive the API the way any client would, run with `make test-e2e`
  against a freshly rebuilt database. They assert the properties that matter rather than the
  shape of the code: that a wrong password and an unknown account are indistinguishable, that
  someone outside an organization is told it was not found rather than that they lack access,
  that a forwarded invitation cannot be redeemed by the wrong person, and that no technical
  detail appears in any error a client receives.
- Servers can be recorded, each either managed or watched only, with the survey of what is
  already on them stored alongside. Setup progress is recorded step by step so a long wait can
  be shown rather than hidden behind a spinner.
- Values that must be stored but never readable from the database alone, such as a customer's
  SSH key, are encrypted with a purpose label, so a value stored for one use cannot be read
  back through a path meant for another.
- A versioned contract between the control plane and the agents on customer servers, designed
  so the two can be upgraded in any order. Agents report what they are capable of rather than
  relying on version comparisons, unknown fields and message types are ignored instead of
  causing a failure, and watch-only is carried in the contract so the agent itself enforces it.
- Connecting to a server and surveying it without changing anything: what it is, what is
  listening and which process holds each port, which containers, images and volumes exist,
  which services are running, and what looks like a database. A server already in use is
  reported as it actually is rather than presented as empty.
- Where a port is held by a container, the container is named rather than the proxy process,
  because "the container called their-nginx" can be acted on and "docker-proxy" cannot.
- Detected databases carry how confident the detection is, since recognising one is a guess by
  nature. Nothing found this way is ever acted on automatically.
- Only Ubuntu and Debian are accepted for now, and an unsupported server is told so plainly
  rather than half-working.
- Connecting a server from the API: it is recorded, then looked at in the background, with
  progress written step by step in plain language so a wait of minutes shows what is happening
  rather than a spinner. Everything found is stored and can be listed, ours and theirs alike.
- A server whose ports 80 and 443 are already taken stops and waits for a decision instead of
  assuming one. A server being watched only finishes at the survey, with nothing installed.
- Background work runs through a durable queue, so a control plane restart does not lose a
  half-finished setup. Job records hold identifiers only and never credentials.
- Disconnecting a server forgets it here and changes nothing on the machine, so leaving costs
  a customer nothing they were running.
- Screens for connecting a server and seeing what is on it. Setup progress appears step by
  step while it runs. What we manage and what was already there are listed separately, so
  ownership is never ambiguous, alongside detected databases, listening ports and running
  services.
- The port question is asked in plain language with the real consequence of each answer
  spelled out, and once answered the choice is shown rather than asked again.

### Security

- Row level security now covers **every** table, including organizations, accounts and
  sessions. Previously those three had no policies, so a query missing its filter could have
  listed every organization or every email address in the system. Nothing tenant-bearing is
  readable without an explicit scope, and a test asserts this for each table so a new table
  cannot quietly ship without policies.
- Accounts are visible only to themselves and to members of the organization currently in
  scope. Sessions are visible only to the account that owns them.
- The three operations that necessarily happen before a caller is identified — verifying a
  password, resolving a session token, and signing out — each go through one narrow
  `SECURITY DEFINER` function rather than relaxing any policy. Each is authorized by
  presenting a secret and returns at most one row.
- The application database role must be `NOSUPERUSER` and `NOBYPASSRLS` and must not own the
  tables. Superusers and table owners bypass row level security, which would silently
  disable tenant isolation.
- Invitation redemption uses a single narrowly scoped `SECURITY DEFINER` function that
  returns at most one row and only to a caller presenting the secret token. It is the only
  sanctioned path around a tenant policy.
- Invitations are bound to the address they were sent to, so forwarding a link does not let
  someone else join. Tokens are single-use and stored only as hashes.
- An admin can manage members but cannot create an owner, so the role cannot be used to
  escalate. An organization can never be left without an owner.
- Asking for an organization you do not belong to reports that it was not found rather than
  that you lack access, so responses cannot be used to discover which organizations exist.
