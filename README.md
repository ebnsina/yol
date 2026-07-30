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

Run `make help` to list every target.

## License

Not yet licensed. All rights reserved.
