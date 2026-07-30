# Development harness

Stand-in servers for developing against, so nothing here needs a real machine or costs
money. Each one is deliberately faithful to a real target: Ubuntu 24.04 with systemd, an
SSH daemon and Docker, which means the bootstrap path exercised locally is the same one that
will run on a customer's server.

## Starting up

```sh
make dev-db      # Postgres on 5442
make migrate-up
make vps-up      # three stand-in servers, first run builds the image
make vps-status
```

| Server | SSH | HTTP | HTTPS |
| ------ | ---- | ---- | ----- |
| vps-1  | 2201 | 8101 | 8401  |
| vps-2  | 2202 | 8102 | 8402  |
| vps-3  | 2203 | 8103 | 8403  |

Credentials are `root` with the key at `dev/fake-vps/keys/id_ed25519`, or the password
`yol-dev`. Both paths exist because bootstrap supports either. The key is generated on first
run and is not committed.

```sh
make vps-ssh N=2     # shell on vps-2
make vps-reset       # destroy them and their disks
```

Three servers rather than one, because multi-machine scheduling arrives later and it is far
easier to design for if it can be run today.

## What has been verified here

- systemd reaches `running`, and units can be installed, enabled and started.
- Docker runs inside, pulls images and runs containers.
- A container published on port 80 is reachable from the host.
- After a restart, standing for a reboot: systemd re-starts enabled units, the image cache
  survives, and a container with a restart policy comes back.
- A container **without** a restart policy stays down after a reboot. This is correct and
  worth keeping in mind: it is exactly the gap the agent's reconcile loop closes, and the
  harness reproduces it faithfully rather than hiding it.

## Testing against a server already in use

The interesting case is a server that is not fresh. `make vps-messy` gives vps-2 their own
nginx on ports 80 and 443, a hand-run Postgres, a container that has since died, and a stray
volume. `make test-live` then checks that a survey reports all of it, blames the right
container for the port conflict, treats none of it as ours, and — most importantly — changes
nothing at all, verified by comparing the machine before and after.

## Checking the promises

`make verify-phase1` runs the guarantees this product makes about a customer's server against
real machines from this harness. It asserts, among others, that looking at a server changes
nothing on it, that a server already in use is reported as it is, that their containers survive
setup and reconciliation, that a container carrying our label is removed while theirs are not,
that logs stream from a container we did not create, and that a watched server has nothing
whatsoever created on it — compared byte for byte before and after.

`make verify-phase2` runs the promise a deploy makes: a commit becomes an image built on the
server itself, what was built is what answers, replacing it drops no requests at all, a version
that never answers fails the deploy with the old one left serving, and going back to a previous
version rebuilds nothing.

Both rebuild the database and reinstall the agent on the stand-in servers, so they take a few
minutes and should not be run against anything you care about.

## Standing in for GitHub

`dev/fake-github` answers as GitHub does for the parts a deploy uses: a token for an
installation, the repositories it covers, what a branch points at, and the code as an archive.
It serves two commits differing only in what the app answers with, which is what makes "did
traffic actually move" a question with an answer.

Nothing in the control plane or the agent behaves differently: the address of GitHub is
configuration (`YOL_GITHUB_API_URL`), and `verify-phase2` points it at the stand-in. So a deploy
can be proven end to end with no repository, no installation and no network.

## The control plane's address

`YOL_PUBLIC_URL` is written into the agent's service file during setup, so it has to be an
address the **server** can reach, not the address the control plane happens to listen on.
Locally that means `http://host.docker.internal:8080`, because `localhost` inside a stand-in
server is the stand-in server itself. Getting this wrong produces an agent that installs
correctly and then never connects, which setup now reports rather than leaving a server
apparently installing forever.

## Notes

These containers are `privileged` with the host cgroup namespace, which systemd and Docker
inside a container both require. That is acceptable for a local harness and is the reason
this image is never published.

`/var/lib/docker` **and** `/var/lib/containerd` are both on volumes. Docker keeps image
snapshots under `containerd`, and overlay cannot be stacked on overlay, so without the second
volume no container can start at all.
