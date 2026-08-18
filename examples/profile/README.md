# examples/profile — a fifth profile, out of tree

This directory is its own Go module (`example.test/acmesim`), not a package of the root
`servicesim` module — that separation is the point. It exists to prove, on every push, that "a
consuming team can write a profile against nothing but `github.com/c360studio/servicesim`'s
exported packages, in their own repository" is a fact CI checks rather than a claim the docs make.
`docs/building-a-profile.md`'s code blocks are literal excerpts of `acme/*.go`, kept in sync by a
test — read that guide first if you are writing your own profile; read this module if you want to
see the guide's code actually compile and pass.

## What is in here

`acme/` is the profile: a fictional vendor with two routes
(`profile.go`, `handler.go`, `render.go`, `errors.go`), its own embedded contract bundle
(`acme/contracts/`), and the tests a consuming team would write against it
(`acme_test.go`, `conformance_test.go`). `main.go` composes it with one of servicesim's own
reference profiles (`profiles/exa`) into a binary through `servicesim.Main` — the same call a real
consumer's `main.go` makes. `Dockerfile` is that binary's own two-stage build, in the shape of the
root `Dockerfile`; it is not built by this repository's CI (the image job builds only
`servicesim`'s own image), but `go build ./...` inside this module is a gate, wired into
`Taskfile.yml`'s `test:profile` task and into `.github/workflows/ci.yml`'s Test, Lint and Build
jobs — a fifth profile that stopped compiling would fail the same push that broke it.

## Running its tests

```bash
cd examples/profile
go build ./...
go vet ./...
go test -race -count=1 ./...
```

The image builds from the **repository root**, not from this directory, because the `replace` below
means the servicesim checkout has to be inside the build context:

```bash
docker build -t acmesim:dev -f examples/profile/Dockerfile .
```

The `go.mod` here `replace`s `github.com/c360studio/servicesim` with `../..` — the only line that
would not appear in a real adopter's own `go.mod`, which would instead pin a tagged release. That
substitution is what lets this module's tests run against the exact servicesim commit under
review, not the last tag, every time `task test` or CI runs them.

## How this maps to the guide

`docs/building-a-profile.md`'s "step 1" through "step 5" each name a file in `acme/` and quote
lines out of it directly. If you are following the guide, `acme/profile.go` is "step 2, write the
package" in full, `acme/contracts/` is "step 1, verify the contract first", and `acme_test.go`
is "step 4, tests" — the four reference profiles the guide names as further worked examples
(`profiles/tavily`, `profiles/exa`, `profiles/mcp`, `profiles/perplexity`, in the guide's stated
order of complexity) live one level up, in the main module, for when Acme's two routes are not
enough of a pattern to copy from.
