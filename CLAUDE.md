# CLAUDE.md

## Code Ordering

Order code for reading, not for hiding implementation details.

Start each file with the external touchpoints a reader is most likely to look
for, usually constructors, exported types, exported functions, or the main
entrypoint for that file.

Before an important block appears, place the things it depends on close above
it. When reading a function signature or body, a reader should already have seen
the relevant local types, options, interfaces, constants, and helpers, or be able
to find them nearby.

Prefer this shape:

1. Imports.
2. Local interfaces and structs needed by the first public entrypoint.
3. Option/config types used by the constructor.
4. Constructor or primary exported entrypoint.
5. Exported options/config helpers.
6. Internal helpers needed by the next public method.
7. The public method that uses those helpers.
8. Repeat helper-before-caller ordering for the rest of the file.
9. Compile-time interface assertions at the bottom.

When choosing between two possible placements, put the dependency nearest to the
first code block that makes the reader need to understand it. Avoid layouts that
force readers to scroll down to discover a helper, then scroll back up to resume
the main flow.

Do not group all private helpers at the bottom by default. That style makes the
file look tidy superficially, but it often makes the actual reading path worse.

### Nearest dependency first

Place dependencies in **reverse order of their first appearance** inside the
block that uses them. The dependency a reader encounters first should be
immediately above the block (nearest); dependencies encountered later sit
further up. This minimises scroll distance: when a reader hits an unfamiliar
type, it is just a short scroll back.

Applied to a struct: the type of the first field goes directly above the struct,
the type of the second field goes above that, and so on.

```go
// SessionService dependency fields in order: threadClient, userClient,
// speakerResolver, timeHarness (builtin scalars omitted).
// So nearest → farthest matches that order in reverse:

type ZoneResolver struct { ... }       // used by timeHarnessConfig (field 4's value-source)
type timeHarnessConfig struct { ... }  // field 4: timeHarness *timeHarnessConfig
type SpeakerResolver struct { ... }    // field 3: speakerResolver *SpeakerResolver
type userClient interface { ... }      // field 2: userClient userClient
type threadClient interface { ... }    // field 1: threadClient threadClient  ← nearest
type SessionService struct {
    threadClient     threadClient
    userClient       userClient
    speakerResolver  *SpeakerResolver
    // … builtin scalars (msgHistoryLength, instructionKey) …
    timeHarness      *timeHarnessConfig
}
```

Applied to a function: the parameter or return type encountered first in the
signature goes immediately above; earlier-encountered local deps are above that.

```go
// NewSessionService signature mentions Option first, then *SessionService.
type SessionService struct { ... }  // encountered second → further up
type Option func(*SessionService)   // encountered first → nearest
func NewSessionService(..., opts ...Option) *SessionService { ... }
```

### Exported helpers that produce option values

Functions like `StaticZone` or `ZoneFromContext` that exist to produce a value
passed into a `With*` option are **option helpers**, not config types. Place them
**after the constructor**, grouped immediately before the `With*` option that
consumes them:

```go
func NewSessionService(...) *SessionService { ... }

func WithMessageHistoryLength(n int) Option { ... }

// Speaker name helpers come here, just before WithSpeakerResolver.
func StaticName(name string) *SpeakerResolver { ... }
func NameFromContext() *SpeakerResolver { ... }

func WithSpeakerResolver(speaker *SpeakerResolver) Option { ... }

func WithInstruction(key string) Option { ... }

// Zone helpers come here, just before WithTimeHarness.
func StaticZone(timezone string) *ZoneResolver { ... }
func ZoneFromContext() *ZoneResolver { ... }

func WithTimeHarness(zone *ZoneResolver) Option { ... }
```

Do not place them before the constructor simply because they return a type that
is declared in the config block.

Keep behavior-preserving reorder commits clean: avoid mixing ordering changes
with renames, refactors, logic changes, or formatting churn outside the touched
file.

## Naming

Name a field by the role it plays in its struct, precisely enough that it cannot
be mistaken for a different concept. When classifying a new struct field, decide
which of the three categories below it belongs to first.

### Dependencies that are API clients → `Client`

Suffix the field and its interface with `Client` (`threadClient`, `userClient`).
A bare `thread`/`user` reads like an identity (a thread id, a user) rather than
the client used to reach one. The field name may equal the interface type name;
that is fine in Go.

### Single-behavior value sources → `Resolver`

A type whose whole job is to produce one value — possibly from `context`,
possibly fallibly — is a behavior, so name it with the agent-noun suffix
`Resolver` (`ZoneResolver`, `SpeakerResolver`), as the standard library does
(`net.Resolver`). It is *not* the value it yields: a `SpeakerResolver` is not a
speaker, it resolves one.

- Construct it through fluent factories named for the **value it yields**, not
  the resolver type or the mechanism: a `ZoneResolver` yields a zone, so
  `StaticZone`/`ZoneFromContext`; a `SpeakerResolver` yields a name, so
  `StaticName`/`NameFromContext`. Name the factory after the value, never repeat
  the resolver/option word, or the option DSL stutters. Compare
  `WithSpeakerResolver(NameFromContext())` (clean) with
  `WithSpeakerResolver(SpeakerFromContext())` (Speaker…Speaker). This mirrors how
  `WithTimeHarness(StaticZone(...))` reads — option word (`Time`) ≠ value word
  (`Zone`).
- Keep the resolver opaque: an unexported
  `resolve func(context.Context) (T, error)` field forces construction through
  the factories (and lets a `With*` option panic on a zero `&XResolver{}`).
- Name the holding field with the same `Resolver` suffix (`speakerResolver`,
  `zoneResolver`) so a `nil` reads as "no custom resolver → default", not "no
  speaker/zone". `field *XResolver` is normal Go (cf. `logger *log.Logger`), not
  stutter to avoid.

### Optional read-path features → `Harness`

A feature that writes framing blocks into session State under consumer-provided
keys on the read path (`buildContext`/`Get`) is a `…Harness` (`timeHarness`).
The consumer includes `{key?}` placeholders in their agent instruction; the ADK
runner resolves them at call time. Enabling or disabling a harness feature does
not require touching the agent instruction — set or omit the key.

Back a harness with a `…HarnessConfig` pointer when it needs two distinct nil
levels: `nil` = disabled, vs non-nil with a nil inner value = enabled with a
default (e.g. `WithTimeHarness(nil)` is a real state). A plain value source that
needs no such second level (a `*Resolver`) is not a harness and takes no wrapper
— symmetry there would only add an empty layer.

When a harness has internal options, use a `…HarnessOpt` function type rather
than additional top-level `Option` functions. This keeps harness-specific
configuration scoped to where the harness is configured:

```go
type TimeHarnessOpt func(*timeHarnessConfig)

func WithTimeHarness(zone *ZoneResolver, opts ...TimeHarnessOpt) Option { ... }
func WithSomeHarnessDetail(v string) TimeHarnessOpt { ... }
```

Note: the state key that instruction blocks are written under is a top-level
concern (`WithInstruction`), not a harness concern. Use `…HarnessOpt` only for
configuration that is truly internal to one harness and has no meaning outside
it.
