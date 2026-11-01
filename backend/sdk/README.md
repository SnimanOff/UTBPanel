# UTBPanel SDK

The SDK is a separate Go module. `sdk.New()` returns immediately. Without
`TGPANEL_SDK`, optional functionality is autonomous; an unavailable panel
never prevents a bot from starting. Close the kit when shutting down.

```go
kit := sdk.New(sdk.WithModules("metrics"))
defer kit.Close()
```

The core supplies `TGPANEL_SDK=unix:///run/tgpanel/sdk/<id>.sock`.
The connection uses the versioned `sdk/wire` transport and reconnects with
backoff. Module registrations are repeated after reconnection. `Wait(ctx)`
is available for operations that require a registered connection.

Importing the root package does not link SQLite. The optional database
module must distinguish `Configured()` from `Connected()`: when a configured
core is unavailable it must fail, never open a local database as a fallback.
