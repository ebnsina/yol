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
- Hostnames are routed to containers by configuring the router over its own interface rather than
  by writing files and restarting it, so changing where a hostname points never interrupts what is
  already being served. Apps are reached by name over a private network, so nothing has to be
  published to the machine to be served on the web.
- Certificates are obtained as hostnames arrive rather than being listed in advance, which is what
  lets a custom domain start working without reconfiguring anything.
- A stand-in for GitHub, so the whole path from a push to a running app can be proven with no
  repository, no installation and no network. The address of GitHub is configuration, so nothing in
  the control plane or the agent behaves differently when it is pointed at the stand-in.
- `make verify-phase2` puts a deploy through its promises against a real machine: a commit becomes
  an image built on that machine, what was built is what answers, replacing it drops no requests, a
  version that never answers fails the deploy with the previous one left serving, and going back
  rebuilds nothing.
- A custom domain needs nothing bought from us: the name is the customer's and so is the server, so
  adding a hostname is what turns HTTPS on. The record to create is spelled out rather than
  described, and the hostname is only served once it is shown to point at that server — otherwise a
  certificate would be requested for a name somebody else controls.
- No free subdomains are handed out yet. An app is reached by the address of the server it was
  placed on, over plain HTTP, and the interface says so plainly rather than implying HTTPS is
  available: no certificate authority issues for an address. The parent name to hand subdomains out
  from is configuration, so buying one later fills a value in rather than changing how any of this
  works.
- A variable may now be required to be present while allowed to be empty, which is how "we have no
  domain yet" is stated as a choice rather than looking like a value that went missing.
- Each screen is a row of sections rather than one long scroll: a server shows what is running, what
  was already there, and its history; an environment shows its deployments, its variables and its
  settings. The chosen one is marked by a rule under it, which needs no colour to read as chosen.
- Icons on every heading, tab and row, so a section is recognisable before it is read.
- Project screens: a list, a project with its environments, and one deploy with its output. An
  environment shows which server it runs on and which branch it follows, both changeable in place,
  and says plainly when it has nowhere to run rather than offering a deploy that would fail.
- Variables are added and removed per environment. Names and when each changed are listed; a value
  is written once and never shown again, because the API does not send one back.
- A deploy is followed as it happens, asking only for output that has arrived since the last line
  on screen, and stops asking once the deploy has finished. A failed one says that whatever was
  serving before still is.
- Going back to a previous version is offered on the version itself, where somebody looking at what
  broke already is.
- Deploying by hand means the same thing as pushing: the head of the branch the environment follows
  is looked up and built, rather than whatever a client happened to send.
- Rolling back runs an image already on the machine, so it takes seconds rather than a build. It is
  recorded as a new attempt rather than reviving an old one, so the history reads as what happened,
  and it is health-gated like any other rollout.
- A deploy's history and output can be read: recent attempts per service, one attempt with the
  reason it failed, and its output from a given moment onwards so a build can be followed without
  reading the whole thing again each time.
- Code comes from GitHub. Access is granted by installing an application, which is confirmed with
  GitHub rather than taken from whatever the browser came back with, and a project is pointed at one
  of the repositories that installation covers.
- A push deploys. The branch an environment follows decides which one, so pushing to the production
  branch deploys production and pushing to the staging branch deploys staging. A push to a branch
  nothing follows does nothing at all.
- Access being taken away on GitHub is recorded when it happens, so a project can say why it stopped
  deploying rather than failing on the next push with nothing to explain it.
- Projects, with a production and a staging environment created alongside each one. Wanting
  somewhere to try a change before it reaches the public is the normal case, and adding the second
  later would mean copying settings across by hand. Each environment follows its own branch and is
  assigned its own server.
- How a service is checked and what it may consume are settable per service, since only whoever
  wrote the app knows which path answers and how much memory it needs.
- Environment variables are encrypted before they are stored and are never sent back. A client can
  see which names are set and when each last changed, and that is all there is to see.
- Deploys do not drop requests. The new version is started alongside the one serving, and traffic
  moves only once it answers; the previous version is left running for a few seconds afterwards so
  requests already in flight finish. A container is named for its deployment, which is what lets
  two versions run at once.
- A version that starts but never answers fails the deploy and is taken away, with the version
  already serving left exactly as it was. Nothing at all is removed on a pass where something
  failed to come up, because a working old version is worth more than a tidy machine.
- A service is checked by asking for the health path it named, or by connecting to its port when it
  named none — starting a container proves nothing on its own.
- Images are built on the customer's own server. Their code never reaches us and no build fleet
  runs on their behalf, so a deploy costs them nothing beyond the server they already pay for. A
  Dockerfile in the repository is always used as written; without one, how to build the app is
  worked out from its files.
- Build output is streamed while it happens and kept afterwards, so a build that failed overnight
  can still be read. A build that could never be started says so rather than pointing at output
  that was never produced.
- The last few images of each app stay on the machine, so rolling back runs one that is already
  there rather than building again.
- An app is reachable by the address of the server it was placed on before any domain has been
  added to it, so a first deploy can be opened straight away. A hostname always wins over this
  once one exists, and on a machine running several apps an address serves nothing, because it
  does not say which of them was meant.

### Security

- A webhook is only acted on once it carries a signature made with the secret only GitHub and we
  hold, compared in constant time. Nothing is said about why one was refused: an address anybody can
  post to should not help somebody work out what a valid delivery looks like.
- The credential that reads a repository is minted per deploy, lasts an hour, and is narrowed to
  reading contents and metadata, so one that leaks from a build cannot write to anybody's
  repository. It is never stored.
- A build request is signed like a specification is, because it hands over a credential for the
  repository and causes code to run. An unsigned one is refused, so reaching the connection is not
  enough to start a build on somebody's server.
- The credential for fetching code is minted for one build, sent as a header rather than in an
  address so it stays out of logs, and never written to disk.
- Code arrives as an archive of a single commit, and the archive is not trusted with where it
  unpacks to. An entry naming a path outside the build is refused rather than quietly rewritten,
  and links are left out entirely, since one could point at the agent's own credential.
- Every build runs inside a builder with memory and processor limits, so a build cannot starve the
  site it is being deployed for. Limits passed to a plain build command are silently ignored by the
  build engine, so this is the only way they hold.
- The tool that works out how to build an app is pinned to a version and checked against a known
  digest before it is installed, so a replaced release is not picked up automatically across every
  customer's machine.
- A customer's server will not obtain a certificate for a hostname until the control plane
  confirms somebody added and verified it. Without that, anyone could point a name they own at a
  customer's machine and make it request certificates on their behalf. The check refuses when it
  cannot reach the control plane: a certificate not obtained can be retried, while one obtained
  for a name we do not control cannot be taken back. An address is refused outright, since a
  certificate is only ever issued for a name.
- The router's control interface is bound to the machine itself, so only the agent can change what
  the router serves, even though ports 80 and 443 are open to the world.

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

### Fixed

- Changing where a service listens broke the app that was already serving. Traffic was pointed at
  whatever port the service currently named, rather than the port the running version was rolled out
  with, so a setting meant for the next deploy took effect on the last one immediately. A version's
  port and health path are now recorded when it is placed, alongside its container name, and belong
  to it for as long as it runs.
- A deploy could never have finished: the container of a version going out was left out of what a
  server was told to run until that version was live, and it could only become live once the server
  had started it and reported that it answered. Both the version serving and the one going out are
  now in the desired state, with traffic pointed at whichever is serving until the new one is
  reported as answering. Found by running a real deploy rather than by any test of a part.
- Setting a deployment's status never worked: the status was compared both as text and as its own
  type in one statement, which the database refuses. Found by the first test to run it.
- A deployment was marked live before the one it replaced stepped aside, which the rule allowing a
  service only one live deployment refuses. They now happen the other way round, in one
  transaction, so no reader sees the moment in between.
