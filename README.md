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
| Every call in a fresh runtime, as in The Things Stack (`KeepState` to reuse) | yes (v0.2.0) |
| The whole result: data together with errors, other keys, empty lists (`Result`) | yes (v0.2.0) |
| Per-script memory and CPU accounting | per worker process, one script each (`Codec.Usage`: peak resident size, CPU time; v0.2.0); in process the memory sample is process-wide |
| Worker sandbox on Linux: seccomp deny list, user, network, mount, IPC and UTS namespaces | yes (`Limits.Seccomp`, `Limits.Namespaces`, v0.2.0) |
| Worker processes on Windows (job objects) | no: process isolation runs on Unix only; not built for lack of a Windows machine to test on |
| The Things Network v2 `Decoder`, `Converter`, `Validator`, `Encoder`; ChirpStack v3 `Decode`, `Encode` | yes, when the script has no decodeUplink or encodeDownlink (v0.2.0) |
| Node-style `Buffer` | yes, with `Limits.NodeBuffer` (v0.2.0) |
| Finding a device's codec in the lorawan-devices repository (`devicerepo`) | yes (v0.2.0) |
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
| `Buffer` | yes, optional | no | yes | no |
| Legacy entry points (TTN v2, ChirpStack v3) | yes | no (only the current names; tested) | ChirpStack v3 `Decode`/`Encode` | n/a |
| Makers' examples from lorawan-devices (4270) | 4165 pass; the 105 others fail in The Things Stack's runner too (tested, below) | 4162 pass (run here as the reference) | not compared | n/a |

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
| `jscodec` (module root) | `New` (compile a script), `Codec.Decode`, `Encode`, `DecodeDownlink`, `Normalize`, `FrameLength`, `Usage`; `Limits`; process isolation (`Process`, `WorkerCommand`, `RunWorker`, `RunWorkerWith`) |
| `devicerepo` | `Open` a copy of lorawan-devices; `Vendors`, `Devices`, `Device`, `Lookup` (vendor, device, firmware, region to codec), `Codec`, `Scripts` |
| `ttn` (nested module, tests only) | fetches every device maker's codec from lorawan-devices at a pinned commit, runs them against their own examples, and compares with The Things Stack's runner |

## Testing

```
go test -race ./...
```

Tests carry `// Covers: SIG-...` comments: these are requirement IDs of
the SigmaM2M project's compliance report and have no meaning outside it.

### Interop tested against

Tested against every codec of
[TheThingsNetwork/lorawan-devices](https://github.com/TheThingsNetwork/lorawan-devices)
at commit `900afb52024f` (2026-10-09), fetched at test time, each run
against the examples its maker published in the codec YAML file. The
codecs are not included in this repository (lorawan-devices has no
LICENSE file): the nested test-only module `ttn` downloads them from that
commit into the user cache directory and checks each file's SHA-256
against `ttn/testdata/files.sha256` (see `ttn/testdata/SOURCE.txt`):

```
cd ttn && SIGMA_JSCODEC_TTN=1 go test -v ./...
```

Without `SIGMA_JSCODEC_TTN`, or offline, the tests skip and say why;
`SIGMA_JSCODEC_TTN=<dir>` uses a checkout of that commit instead. Every
example runs in process and again in worker processes (`-short` skips the
second pass); both give the same results.

On 2026-10-12 (v0.2.0): all 879 codec files, about 700
of them with examples, 4270 examples in all (decodeUplink 2206,
encodeDownlink 1148, decodeDownlink 883, normalizeUplink 33). 4165 pass,
in process and in worker processes alike. The 105 others are listed in
`ttn/known_test.go` with the reason The Things Stack's runner gives, as
it fails each of them too: 58 scripts that do not compile (literal `\n`
sequences in place of line breaks), 29 outputs that differ from their
examples, 11 scripts with only the TTN v2 `Decoder` (which The Things
Stack does not run; this module does, and passes 3 more examples that
way), and a few that throw. 31 scripts named by codec files do not load
(29 do not compile, one is missing, one has no entry point), listed in
`knownLoadFailures`.

The Things Stack v3.36.2's JavaScript runner, the one the repository's
validator uses, runs every example as well (`TestAgainstTheThingsStack`,
in Docker, `SIGMA_JSCODEC_TTS=1`): every example it passes, this module
passes; every known failure, it fails. The comparison found four
differences, now fixed: state kept between calls (a script's globals),
recvTime added when the example gives none, recvTime given as a YAML
timestamp, and `instanceof` on typed array constructors.

v0.1.0 ran 57 codec files of 11 makers (244 examples, all passing).

Results are compared as the repository's own validator does: data and
warnings must equal the example once both are plain JSON (numbers by
value); an example with errors must end the call with a `DecoderError`
carrying exactly those messages; for encodeDownlink the bytes, fPort and
warnings must match; since v0.2.0 also keys beside those, through
`Result`, and data or warnings returned together with errors. Lists the
example leaves empty are compared by value only. A second
test alters each passing example's expectation and checks that the
comparison then fails. The test fails when an example outside the
known-failure list (`ttn/known_test.go`) fails, or a listed one passes.

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
do not trust with `Isolation: Process`, and on Linux add `Seccomp` and
`Namespaces`.

## Licence

Apache-2.0 (see [LICENSE](LICENSE) and [NOTICE](NOTICE)). Copyright 2026
Abhinav Gautam. The Things Stack and The Things Network are trademarks of
their owner; this module is not made or endorsed by them.
