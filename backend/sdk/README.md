# UTBPanel SDK

The SDK is a separate Go module. Without `TGPANEL_SDK`, `sdk.New()` returns
immediately and optional functionality is autonomous; an unavailable panel
never prevents a bot from starting. Close the kit when shutting down.

```go
kit := sdk.New()
defer kit.Close()
// Migrations and warmup go here.
kit.Ready()
```

The core supplies `TGPANEL_SDK=unix:///run/tgpanel/sdk/<id>.sock`.
The connection uses the versioned `sdk/wire` transport and reconnects with
backoff. Module registrations are repeated after reconnection. `Wait(ctx)`
is available for operations that require a registered connection.

Readiness is enabled by default. With a configured core, New registers this
requirement before returning (up to five seconds), preventing an open HTTP
port from marking an unprepared bot ready. Call Ready after startup work;
the signal is replayed on reconnect. `sdk.WithoutReady()` opts into ordinary
port-based readiness when only other modules are needed. The core waits up
to `TGPANEL_SDK_READY_TIMEOUT` (default `60s`) for the explicit signal.

Importing the root package does not link SQLite. The optional database
module must distinguish `Configured()` from `Connected()`: when a configured
core is unavailable it must fail, never open a local database as a fallback.

```go
import sdkdb "github.com/SnimanOff/UTBPanel/backend/sdk/db"

db, err := sdkdb.Open(kit) // DATABASE_URL in both modes
// Or import sdk/db and use sql.Open("tgpanel", os.Getenv("DATABASE_URL")).
```

The driver supports transactions, prepared statements, named parameters,
context cancellation, and batched result rows. Each SQL connection is pinned
to its original transport session. A lost write response is returned as an
error and is never automatically retried by the driver.
