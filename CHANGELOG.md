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
- The agent that runs on a customer's server: a single file with no dependencies, about six
  megabytes, which registers itself, holds a connection out to us, and reports what is on the
  machine. Because it dials out, a customer opens no port on their server for us.
- Losing the connection is expected rather than exceptional. The agent retries with a growing
  delay and a little randomness, so a fleet that lost us together does not all return at once,
  and a server comes back on its own without anyone intervening.
- Reading a machine is now one piece of code used both over SSH before anything is installed
  and by the agent afterwards, so a server looks the same however we came to look at it.
- Live logs from any container on a connected server, including ones we did not create, since
  reading changes nothing. History and new lines arrive together, with output and errors kept
  apart, and nothing is stored: watching costs only the connection it travels over.
- One reader falling behind never stalls another, and a chatty container is batched rather than
  producing a message per line.
- Setting a server up now happens by itself: Docker is installed if it is missing, the agent is
  copied over the connection already open, and it is set to start on boot and be restarted if
  it stops. Each step is reported as it happens.
- Docker already present is left exactly as it is, so a server already running containers does
  not have its engine touched.
- Installing waits for the agent to appear, and says what to check if it does not, rather than
  leaving a server apparently being set up forever.
- Nothing is installed until the server has been looked at and setup explicitly asked for, so
  connecting a server and changing it stay separate decisions.
- The agent now keeps a server matching what it has been told to run. It corrects the machine on
  a schedule as well as when told, so a container that stopped, or a reboot, is put right without
  anyone asking. What it was last told is kept on disk, so a server that reboots recovers before
  the control plane has even been reached.
- A router is now run on servers where we handle web traffic, which is the first thing this
  platform runs on a customer's machine. It is given a memory limit, as everything we run is, so
  one container cannot take a whole server down.
- Where a customer's own web server keeps ports 80 and 443, no router is run and their sites are
  left entirely alone.
- A verification suite that checks the promises made about a customer's server against real
  machines: that looking changes nothing, that their work survives, that only what we created is
  ever removed, that logs stream from containers we did not create, and that a watched server has
  nothing created on it at all. Run with `make verify-phase1`.
- Projects, with an environment for each running copy — production, staging, or any name — each
  pointed at a server, which may be the same one or a different one. Environment variables are
  encrypted, and their values are never returned once set.
- Deployments keep the image they built, so rolling back reuses it rather than building again.
  Only one deployment is live for a service at a time, which the database itself enforces.
- Where a deployment runs is a row rather than a column, so a service spanning several machines
  later is an addition rather than a change.
- Host ports are allocated by the control plane, so two projects on one server cannot collide.
  Ports something else on the machine is already using are recorded and never handed out, asking
  twice for the same purpose returns the same port so a retried deploy does not consume another,
  and a full range says so plainly rather than failing obscurely.

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
- Instructions to change a server are signed by the control plane and checked by the agent, so
  reaching the connection is not the same as being allowed to run containers on someone's
  machine. The signature is asymmetric: servers hold only the public half, so a compromised
  machine cannot forge instructions for any other.
- What the agent keeps on disk is stored with its signature and checked again when reloaded,
  rather than trusted because it came from our own files.
- Removal only ever considers containers this platform owns, recognised by our label or named as
  adopted in the instruction. A customer's containers are not merely spared, they are absent from
  the list the removal step reads. Proven on a real server: a container carrying our label was
  removed while three of theirs, and their volume, were untouched.
- Watch-only is decided by the agent, and only an explicit instruction that a server is managed
  permits a change. An agent that has not been told its mode, or fails to learn it, can change
  nothing rather than everything.
- Log requests name a container chosen in the control plane, so the agent passes it to Docker
  as an argument rather than through a shell, and a container named to look like a command
  cannot run one.
- An agent registers with a single-use token that is consumed as it is used, and trades it for
  a lasting credential kept readable only by its owner. A token seen during setup cannot be
  replayed afterwards, and only the hash of the lasting credential is stored.
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
