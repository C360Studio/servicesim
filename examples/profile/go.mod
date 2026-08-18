// This is a separate Go module, deliberately: it proves that a profile can be
// written, built and tested with nothing but github.com/c360studio/servicesim
// pinned as an ordinary dependency, the way a real adopter's repository would
// pin it. The replace directive below is the only thing that would not
// appear in an adopter's own go.mod — it points at this checkout instead of a
// tagged release, which is what lets `task test` and CI prove this module
// against the servicesim commit under review rather than the last release.
module example.test/acmesim

go 1.26.4

require github.com/c360studio/servicesim v0.0.0

require (
	github.com/google/go-cmp v0.7.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/c360studio/servicesim => ../..
