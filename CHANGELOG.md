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
