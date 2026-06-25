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
// SessionService fields in order: thread, user, knowledge, timeHarness.
// So nearest → farthest matches that order in reverse:

type Zone struct { ... }            // used by timeHarnessConfig (field 4's type)
type timeHarnessConfig struct { ... } // field 4: timeHarness *timeHarnessConfig
type knowledgeConfig struct { ... }   // field 3: knowledge *knowledgeConfig
type userClient interface { ... }     // field 2: user userClient
type threadClient interface { ... }   // field 1: thread threadClient  ← nearest
type SessionService struct {
    thread   threadClient
    user     userClient
    knowledge *knowledgeConfig
    timeHarness *timeHarnessConfig
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

func WithKnowledgeContext(...) Option { ... }

// Zone helpers come here, just before WithTimeHarness.
func StaticZone(...) *Zone { ... }
func ZoneFromContext() *Zone { ... }

func WithTimeHarness(zone *Zone) Option { ... }
```

Do not place them before the constructor simply because they return a type that
is declared in the config block.

Keep behavior-preserving reorder commits clean: avoid mixing ordering changes
with renames, refactors, logic changes, or formatting churn outside the touched
file.
