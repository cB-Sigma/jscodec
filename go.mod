module github.com/cB-Sigma/jscodec

go 1.26.0

require (
	github.com/dlclark/regexp2/v2 v2.5.2
	github.com/dop251/goja v0.0.0-20260917113740-793a2a65c13b
	golang.org/x/sys v0.48.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/go-sourcemap/sourcemap v2.1.3+incompatible // indirect
	github.com/google/pprof v0.0.0-20260802141513-ef3492d7dac3 // indirect
	golang.org/x/text v0.42.0 // indirect
)

// v0.1.0 carried third-party codec files by mistake; see CHANGELOG.md.
retract v0.1.0
