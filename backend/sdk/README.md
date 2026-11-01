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

Core queries use the bot's filesystem identity on Linux and cannot attach
other databases or export through VACUUM INTO. The query timeout is 30 seconds
(`TGPANEL_SDK_QUERY_TIMEOUT`); slow queries over 50 ms are recorded with
literals and comments removed. The Database tab shows counts, active queries,
latency buckets and the last 20 slow queries. Each session allows up to 64 SQL
connections, with 128 statements and 16 result cursors per connection.

Custom metrics:

```go
orders := kit.Counter("orders_created")
temperature := kit.Value("temperature")
orders.Add(1)
temperature.Set(-2.5)
```

Metrics are sent every 10 seconds. Names use letters, digits, underscores,
dots, colons and hyphens (up to 64 characters, starting with a letter or
underscore); there are at most 200 per bot. Counters accept nonnegative
increments, values accept finite numbers. Retried batches are deduplicated.
Without a connection these calls do nothing. Select visible metrics on the
bot dashboard; values, including zero and negative values, go into history.

Scheduled tasks:

```go
if err := kit.Task("digest", "0 9 * * *", func(ctx context.Context) error {
    return sendDigest(ctx)
}); err != nil {
    log.Fatal(err)
}
```

Schedules use UTC. With the core connected, only the main instance runs tasks;
the core records duration and outcome and offers Run now under Settings → Tasks.
Pausing or stopping a bot cancels its tasks. Without a connection, the SDK's
local scheduler runs them; canary instances do not run schedules. Functions
must honor context cancellation. At most 100 names are registered per bot,
and a task never overlaps itself. The core bounds one run to 15 minutes.
