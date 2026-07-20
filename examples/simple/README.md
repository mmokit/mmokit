# Simple MMOKIT example

This is the smallest runnable MMOKIT game in the repository. It demonstrates current process composition without game-specific persistence or generated client code:

- Anonymous player authentication
- A custom Go system that animates and broadcasts a field of entities
- A small static browser client
- The engine-provided admin dashboard

Start with [`main.go`](main.go), then read [`system_field.go`](system_field.go).

## Run

Prerequisites are Go 1.25.1, Just, Docker Compose, Bun, and tmux.

```sh
cd examples/simple
just run
```

The recipe starts PostgreSQL, creates the isolated `mmo_simple` database when needed, builds the admin UI and Go binary, serves this directory, and runs the server.

Open:

- Browser client: <http://localhost:5174>
- Gateway: <http://localhost:8080>
- Admin dashboard: <http://localhost:9101/admin/>

On an empty development database, the admin login is `admin` / `admin`. Change it before exposing the dashboard outside local development.

Press Ctrl-C in the recipe terminal to stop the server and static-file tmux session.

## Composition

The server setup is intentionally short:

```go
process := mmokit.New(mmokit.Config{
    Name:          "simple",
    AnonymousAuth: true,
})

process.AddSystem(mmokit.NewSystem(&FieldSystem{}))
process.Start()
```

`Process.Start` builds the default all-role development process, creates its cell runtime, starts gateway/admin listeners, and blocks until shutdown.

`FieldSystem` shows the current custom-system pattern: embed the non-generic `mmokit.SystemBase`, declare a typed `mmokit.Query` field, and register a zero-value prototype. MMOKIT constructs a fresh system instance for every cell. Its tune-tagged fields control the sine wave at runtime; for example, `tune set Field amplitude 420` changes its height.

## Useful recipes

| Recipe | Result |
| --- | --- |
| `just build-go` | Build `bin/simple` |
| `just admin-build` | Rebuild the embedded admin SPA |
| `just build` | Build admin and Go artifacts |
| `just db-up` | Start PostgreSQL and ensure `mmo_simple` exists |
| `just run` | Build and run the complete example |
| `just clean` | Remove `bin/simple` |

Set `POSTGRES_URL` to override the example database connection.
