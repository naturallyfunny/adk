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

Keep behavior-preserving reorder commits clean: avoid mixing ordering changes
with renames, refactors, logic changes, or formatting churn outside the touched
file.
