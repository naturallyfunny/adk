# Proposal: retarget `adk/tuya` toolset onto the split `tuya` library

Status: **Draft / for review**
Author: Ardian (with Claude)
Date: 2026-06-20
Companion to: `~/dev/tuya/PROPOSAL.md` (the library-side refactor).

## 1. Where we are

`adk/tuya/toolset.go` is written against an **old** shape of
`go.naturallyfunny.dev/tuya` — a single owner-keyed `*tuya.Client` with
`Account` / `ListDevices` / `DeviceStatus` / `SendCommands`, each taking the
human's id as a parameter:

```go
func Tools(c *tuya.Client) ([]adktool.Tool, error) { ... }

// inside the tools:
c.Account(toolCtx, toolCtx.UserID())
c.ListDevices(toolCtx, toolCtx.UserID())
c.DeviceStatus(toolCtx, toolCtx.UserID(), in.DeviceID)
c.SendCommands(toolCtx, toolCtx.UserID(), in.DeviceID, toDataPoints(in.Commands))
```

The library has since split into three pieces (`tuya.Client` transport,
`tuya.IoTClient` device ops, `postgres.Store` account mapping) and grown an
`app.Client` (owner-keyed, batteries-included). The companion proposal moves the
ownership guard out of `IoTClient` and into `app.Client`, and relocates
`ErrAccountNotLinked` / `ErrDeviceNotOwned` into the `app` package. This module
still pins the pre-split version, so it must be retargeted.

## 2. The identity model is already right

The toolset reads the human's identity from the ADK tool context —
`toolCtx.UserID()` — and passes it as an explicit argument. This **is** the
"context-propagated identity" we want: the agent never types a uid or an owner
id; the framework supplies it, the toolset forwards it. `UserID()` is the
consumer's **owner id** (the app's notion of the human), never a Tuya UID.

That single fact decides the design: **the toolset is owner-keyed.** Whatever it
binds to must accept an owner id and resolve the Tuya UID itself. That is exactly
`app.Client`.

## 3. Proposal: accept a consumer-defined interface, bind `app.Client`

Per "accept interfaces, return structs," define the narrow interface the toolset
consumes **here**, and have `Tools` take it instead of a concrete library type:

```go
package tuya // adk/tuya

import (
    "context"

    "go.naturallyfunny.dev/tuya"
    "go.naturallyfunny.dev/tuya/app"
)

// Client is the owner-keyed surface the toolset drives. app.Client satisfies it.
// Every method takes the human's owner id (toolCtx.UserID()); the implementation
// resolves the Tuya UID and enforces ownership.
type Client interface {
    Account(ctx context.Context, ownerID string) (app.Account, error)
    ListDevices(ctx context.Context, ownerID string) ([]tuya.Device, error)
    DeviceStatus(ctx context.Context, ownerID, deviceID string) ([]tuya.DataPoint, error)
    SendCommands(ctx context.Context, ownerID, deviceID string, cmds []tuya.DataPoint) error
}

func Tools(c Client) ([]adktool.Tool, error) { ... } // body essentially unchanged
```

The tool bodies stay as they are — `c.ListDevices(toolCtx, toolCtx.UserID())`,
etc. Only the binding changes:

```go
// consumer wiring
iot   := tuya.NewIoTClient(transport)
store, _ := postgres.NewAccountStore(ctx, pool, postgres.WithAutoMigrate())
appClient := app.New(iot, store) // satisfies adk/tuya.Client
tools, _ := tuya.Tools(appClient)
```

### Why one owner-keyed interface, not two toolsets

Because `toolCtx.UserID()` is **always** the owner id, the toolset has exactly
one identity semantic. There is no second "uid-keyed" toolset to write — a raw
`tuya.IoTClient` (device-addressed, uid for `ListDevices`) does not fit an
owner-keyed surface and should not be force-fit. A consumer who genuinely has no
owner→uid mapping (their app's user id *is* the Tuya UID) has two clean options:

1. Supply a trivial `app.AccountStore` whose `Get` returns
   `Account{OwnerID: id, TuyaUID: id}`, and use `app.Client` unchanged. Ownership
   assertion still applies.
2. Implement the four-method `Client` interface above with a ~15-line adapter
   over `*tuya.IoTClient` that treats `ownerID` as the uid. This opts out of the
   account store but must re-add the ownership check itself (now that
   `IoTClient` no longer carries it).

Either way the **toolset** is single; only the binding differs. The owner↔uid
footgun cannot reach a tool call site: the interface carries one id whose meaning
is fixed by which implementation the consumer wired, decided once at startup.

## 4. Error mapping

`forAgent` currently switches on `tuya.ErrAccountNotLinked` and
`tuya.ErrDeviceNotOwned`. Both sentinels move to the `app` package in the
companion proposal:

```go
case errors.Is(err, app.ErrAccountNotLinked): ...
case errors.Is(err, app.ErrDeviceNotOwned):  ...
```

`tuya.Device`, `tuya.DataPoint`, `tuya.Channel` stay in `tuya` and are unchanged,
so the `toDeviceViews` / `toDataPoints` mappers are untouched.

## 5. Migration impact

| File | Change |
|---|---|
| `adk/tuya/toolset.go` | `Tools(c *tuya.Client)` → `Tools(c Client)` with the consumer-defined `Client` interface; `forAgent` sentinels `tuya.*` → `app.*`; `accountView` maps from `app.Account`. Tool bodies unchanged. |
| `go.mod` | Bump `go.naturallyfunny.dev/tuya` to the post-split + post-relocation version. |
| consumer wiring / `examples` | Build `app.New(iot, store)` and pass it to `Tools`. |
| `adk/tuya/toolset_test.go` | Fakes now implement the `Client` interface (already owner-keyed), assert `app.ErrAccountNotLinked` / `app.ErrDeviceNotOwned`. |

## 6. Open questions

1. **Keep `get_account` as a tool?** It maps cleanly to `app.Client.Account`
   (added in the companion proposal). Useful for the agent to confirm linkage
   before acting. Lean keep.
2. **Should `adk/tuya.Client` live in `adk/tuya`, or be promoted to a shared
   spot?** Postera/spotify toolsets bind concrete library clients directly
   (`*spotify.Client`, `*postera.Postarius`). Tuya is the first to need an
   interface, because it is the first with two plausible backings (app store vs
   bare-uid adapter). Define it locally in `adk/tuya` for now; promote only if a
   second toolset needs the same shape.
3. **Version coordination.** The library change is breaking
   (`IoTClient` signature, sentinel package moves). This module's bump must land
   together with, or after, the library release that carries them.
```
