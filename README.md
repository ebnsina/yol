# yol

Connect a server you already own and get push-to-deploy, managed data services,
and live observability on top of it. Your hardware, your bill, your data.

## Layout

```
cmd/api          control plane HTTP API
cmd/agent        binary installed on managed servers
internal/proto   versioned agent <-> api message contract
internal/db      migrations and generated queries
web              SvelteKit frontend
dev              local server harness for development
```

## Requirements

- Go 1.26+
- Node 24+ with pnpm
- Docker

## Development

```sh
cp .env.example .env    # then fill every value; the API refuses to boot otherwise
make dev-db             # start Postgres
make migrate-up         # apply migrations
make api                # run the control plane
make web                # run the frontend
```

To work on server management, start the stand-in servers as well. They run the same operating
system and service manager as a real target, so nothing here needs a machine of your own:

```sh
make vps-up             # three stand-in servers on ports 2201-2203
make vps-status
```

See `dev/README.md` for details.

```sh
make test        # unit and integration tests
make test-e2e    # drives the API end to end against a fresh database
make lint
```

Run `make help` to list every target.

## License

Not yet licensed. All rights reserved.
