# The test catalog format

A repository describes its tests for Socrates in `.socrates/tests/`: `TESTS.md`, one folder per area
with its `AREA.md`, one Markdown file per test with its `## Steps`, and the tools the tests use. The
reference for writing one - every key, the dimensions, the tool contract, the verdicts and how to
check a catalog - is [internal/testmode/catalog/FORMAT.md](../internal/testmode/catalog/FORMAT.md),
the same text chat agents read with the tool `tests_format`. Check a catalog with
`socrates tests check [repository]`; the design is in [design/TEST-MODE.md](design/TEST-MODE.md).
