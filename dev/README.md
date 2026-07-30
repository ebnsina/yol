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

## Notes

These containers are `privileged` with the host cgroup namespace, which systemd and Docker
inside a container both require. That is acceptable for a local harness and is the reason
this image is never published.

`/var/lib/docker` **and** `/var/lib/containerd` are both on volumes. Docker keeps image
snapshots under `containerd`, and overlay cannot be stacked on overlay, so without the second
volume no container can start at all.
