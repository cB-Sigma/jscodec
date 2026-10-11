// Test-only module: runs device makers' payload codecs and their own
// examples from TheThingsNetwork/lorawan-devices through jscodec. Nothing
// imports it.
module github.com/cB-Sigma/jscodec/ttn

go 1.26.0

replace github.com/cB-Sigma/jscodec => ../

require (
	github.com/cB-Sigma/jscodec v0.2.0-00010101000000-000000000000
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/dlclark/regexp2/v2 v2.5.2 // indirect
	github.com/dop251/goja v0.0.0-20260917113740-793a2a65c13b // indirect
	github.com/go-sourcemap/sourcemap v2.1.3+incompatible // indirect
	github.com/google/pprof v0.0.0-20260802141513-ef3492d7dac3 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)
