# Provenance

## How the code was written

Written for SigmaM2M in Go. No code was ported or copied from another
implementation. The files came from SigmaM2M's codec package (first
commit 2026-09-27, with later fixes) and became this module; the package
was renamed from `codec` to `jscodec`, and the worker's dependency on
SigmaM2M's log redaction became a hook (`RunWorkerWith`; `RunWorker`
passes none). example_test.go was written for this module.

On 2026-10-11 device makers' codecs from TheThingsNetwork/lorawan-devices
were run against their own examples (the nested module `ttn`, which
fetches them at test time from a pinned commit; none is included here).
That run led to `DecodeDownlink`, `Normalize` and the recvTime format.

On 2026-10-12 (v0.2.0) the run took in every codec of that repository
with examples, and every example also went through The Things Stack
v3.36.2's JavaScript runner (`ttn/tts`: a small program of ours, linking
The Things Stack's `pkg/scripting/javascript` in a Docker image built on
the tester's machine) as the reference. That comparison led to fresh
runtimes per call, `OmitRecvTime`, passing other input fields through,
and plain-function wrappers for the typed array constructors. The legacy
entry points, `Buffer`, the Linux sandbox, `Usage` and `devicerepo` are
new.

## Sources

- The script interface (function names, the input object's bytes, fPort
  and recvTime members, the data, warnings and errors members of a result)
  follows The Things Stack's public documentation of JavaScript payload
  formatters. These are interface facts; no code of The Things Stack was
  used.
- The limits and guards are our own design on goja's interrupt API and
  regexp2's match timeout; process limits use setrlimit(2) and Linux's
  /proc/<pid>/statm.
- Legacy entry points: the function names and arguments of The Things
  Network v2 payload functions and ChirpStack v3 codecs, from their public
  documentation.
- Buffer (buffer.go): Node.js's documented Buffer API; no code taken.
- Seccomp and namespaces: seccomp(2), the classic BPF filter format and
  clone(2) flags as the Linux manual pages describe them, through
  golang.org/x/sys/unix.
- devicerepo: the file layout of lorawan-devices, read from its files.
