# Provenance

## How the code was written

Written for SigmaM2M in Go. No code was ported or copied from another
implementation. The files came from SigmaM2M's codec package (first
commit 2026-09-27, with later fixes) and became this module; the package
was renamed from `codec` to `jscodec`, and the worker's dependency on
SigmaM2M's log redaction became a hook (`RunWorkerWith`; `RunWorker`
passes none). example_test.go was written for this module.

On 2026-10-11 device makers' codecs from TheThingsNetwork/lorawan-devices
were run against their own examples (the nested module `ttn`). That run
led to `DecodeDownlink`, `Normalize` and the recvTime format.

## Sources

- The script interface (function names, the input object's bytes, fPort
  and recvTime members, the data, warnings and errors members of a result)
  follows The Things Stack's public documentation of JavaScript payload
  formatters. These are interface facts; no code of The Things Stack was
  used.
- The limits and guards are our own design on goja's interrupt API and
  regexp2's match timeout; process limits use setrlimit(2) and Linux's
  /proc/<pid>/statm.
