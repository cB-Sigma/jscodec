# jscodec

A sandbox for JavaScript payload codecs in the style of The Things Stack's payload formatters (`decodeUplink`, `encodeDownlink`, `decodeDownlink`, `normalizeUplink`), in pure Go.

[![Go Reference](https://pkg.go.dev/badge/github.com/cB-Sigma/jscodec.svg)](https://pkg.go.dev/github.com/cB-Sigma/jscodec) [![test](https://github.com/cB-Sigma/jscodec/actions/workflows/test.yml/badge.svg)](https://github.com/cB-Sigma/jscodec/actions/workflows/test.yml) [![Licence: Apache-2.0](https://img.shields.io/badge/licence-Apache--2.0-blue.svg)](LICENSE) [![Go](https://img.shields.io/badge/go-1.26-00ADD8.svg)](go.mod)

Device makers publish their LoRaWAN payload codecs as JavaScript, and
integrators write their own. This module runs such scripts on goja (an
ECMAScript engine in Go, so no cgo) with every call bounded in time,
stack, memory and the size of what built-ins may build, and optionally in
worker processes under operating-system limits. It adds an optional
`frameLength(bytes)` for byte-stream transports. Written for SigmaM2M, a
oneM2M platform, whose LoRaWAN, Sigfox and raw TCP/UDP adapters use it.

## Features

| Feature | Status |
|---|---|
| `decodeUplink(input)` with bytes, fPort, recvTime, variables and extra fields; data, warnings and errors in results | yes |
| `encodeDownlink(input)`, `decodeDownlink(input)`, `normalizeUplink(input)` | yes |
| `frameLength(bytes)` for stream transports | yes (our addition) |
| `console.log` to an `slog.Logger` | yes |
| In-call limits: timeout, stack depth, array, string and buffer sizes in built-ins, regular expressions with a match timeout, sampled memory allocation | yes |
| Worker processes: pool, crash restart, CPU limit (setrlimit, Unix), data-segment limit (Linux), resident-memory watch (Linux) | yes |
| Exact per-script memory accounting | no: the in-process sample is process-wide |
| seccomp, namespaces, Windows job objects | no |
| v2 (TTN classic) `Decoder(bytes, port)` as an entry point | no (scripts may still define and call it, as many makers' codecs do) |
| Node-style `Buffer` | no (ChirpStack provides one) |
| Modules, timers, network, files | no, by design |

## Choosing a codec runner

Compared from each project's source at the versions named, in October
2026. Check the projects themselves before relying on a row.

| | this module | The Things Stack v3.36.2 (`pkg/scripting/javascript`) | ChirpStack v4.19.2 (`codec/js`) | goja (master 73dc7ed) directly |
|---|---|---|---|---|
| Language of the host | Go | Go | Rust | Go |
| JavaScript engine | goja | goja | QuickJS (rquickjs) | goja |
| Usable as a library on its own | yes | no: a package inside the network server | no: inside the network server | yes: an engine, no codec API |
| decodeUplink, encodeDownlink | yes | yes | yes | write it yourself |
| decodeDownlink, normalizeUplink | yes | yes | no | write it yourself |
| Bounds per call | time, stack, memory sample, built-in sizes, regexp timeout | time | time | interrupt API |
| Process isolation with OS limits | yes | no | no | no |
| `Buffer` | no | no | yes | no |
| Makers' examples from lorawan-devices | 244 of 244 (below) | the repository's own validator | not compared | n/a |

Pick this module to run makers' or users' codecs inside a Go program that
is not a LoRaWAN network server, especially when the scripts are not
trusted. Inside The Things Stack or ChirpStack, their own runners apply;
pick goja directly if you need a general JavaScript engine rather than
this script contract.

## Install

```
go get github.com/cB-Sigma/jscodec@latest
```

Requires Go 1.26 or later.

## Quick start

```go
import "github.com/cB-Sigma/jscodec"

c, err := jscodec.New("demo.js", `function decodeUplink(input) {
  return { data: { temperature: (input.bytes[0] << 8 | input.bytes[1]) / 10 } };
}`, jscodec.Limits{}, nil)
defer c.Close()
d, err := c.Decode(ctx, jscodec.Uplink{Bytes: []byte{0x00, 0xEB}, FPort: 1})
// d.Data["temperature"] == 23.5
```

`Example` in example_test.go runs this and checks its output.

With `Limits{Isolation: jscodec.Process}` the script runs in worker
processes started from the program's own executable with `WorkerCommand`
as the first argument, so `main` must begin with:

```go
if len(os.Args) > 1 && os.Args[1] == jscodec.WorkerCommand {
	os.Exit(jscodec.RunWorker())
}
```

Importing the package sets `regexp2.DefaultMatchTimeout` to 250 ms for
the whole program (goja compiles some regular expressions with regexp2),
so a backtracking pattern cannot run unbounded.

## Documentation

API reference: https://pkg.go.dev/github.com/cB-Sigma/jscodec

| Package | What it does |
|---|---|
| `jscodec` (module root) | `New` (compile a script), `Codec.Decode`, `Encode`, `DecodeDownlink`, `Normalize`, `FrameLength`; `Limits`; process isolation (`Process`, `WorkerCommand`, `RunWorker`, `RunWorkerWith`) |
| `ttn` (nested module, tests only) | runs device makers' codecs from lorawan-devices against their own examples |

## Testing

```
go test -race ./...
```

Tests carry `// Covers: SIG-...` comments: these are requirement IDs of
the SigmaM2M project's compliance report and have no meaning outside it.

### Interop tested against

Real device makers' codecs from
[TheThingsNetwork/lorawan-devices](https://github.com/TheThingsNetwork/lorawan-devices)
(commit `900afb52024f`, 2026-10-09; fetched 2026-10-11), run against the
examples each maker published in its codec YAML file. The nested
test-only module `ttn` carries the codecs unchanged
(`ttn/testdata/SOURCE.txt`):

```
cd ttn && go test -v ./...
```

Every example runs in process and again in worker processes (`-short`
skips the second pass); both give the same results.

| Vendor | Codec files | Examples | Passed |
|---|---|---|---|
| adeunis | 4 | 4 | 4 |
| browan | 4 | 12 | 12 |
| decentlab | 6 | 39 | 39 |
| dragino | 8 | 23 | 23 |
| elsys | 5 | 7 | 7 |
| mclimate | 4 | 12 | 12 |
| milesight-iot | 8 | 21 | 21 |
| netvox | 6 | 75 | 75 |
| sensecap | 4 | 6 | 6 |
| tektelic | 4 | 13 | 13 |
| watteco | 4 | 32 | 32 |
| **total** | **57** | **244** | **244** |

By routine: decodeUplink 148, encodeDownlink 45, decodeDownlink 33,
normalizeUplink 18. Two sensecap files have no examples; their scripts are
only loaded.

Results are compared as the repository's own validator does: data and
warnings must equal the example once both are plain JSON (numbers by
value); an example with errors must end the call with a `DecoderError`
carrying exactly those messages; for encodeDownlink the bytes, fPort and
warnings must match. Not compared, because the API does not return them:
keys a script returns beside data, bytes, warnings and errors, whether an
empty list was present, and data returned together with errors. A second
test alters each passing example's expectation and checks that the
comparison then fails. The test fails when an example outside the
known-failure list (`ttn/known_test.go`, empty today) fails, or a listed
one passes.

The run found and fixed: `decodeDownlink` and `normalizeUplink` were
missing (51 examples), and `recvTime` lost trailing zeros (`.508306410`
became `.50830641`), which broke 29 of watteco's 32 examples since its
codecs copy recvTime into their output; recvTime now has 0, 3, 6 or 9
fractional digits, as The Things Stack writes it.

## Related repositories

None. The sandbox needs no peer, so there is no simulator.

## Status

v0.x: the API may change before v1.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Security

See [SECURITY.md](SECURITY.md) to report a vulnerability privately. The
in-process limits are a guard, not a security boundary; run scripts you
do not trust with `Isolation: Process`.

## Licence

Apache-2.0 (see [LICENSE](LICENSE) and [NOTICE](NOTICE)). Copyright 2026
Abhinav Gautam. The Things Stack and The Things Network are trademarks of
their owner; this module is not made or endorsed by them.
