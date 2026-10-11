# Changelog

## v0.2.0 (2026-10-12)

Added:

- The Things Network v2 (`Decoder`, `Converter`, `Validator`, `Encoder`)
  and ChirpStack v3 (`Decode`, `Encode`) entry points, run as
  `decodeUplink` and `encodeDownlink` when a script lacks those.
- `Limits.NodeBuffer`: a Node-style `Buffer` (as ChirpStack provides).
- `Result` on `Decoded`, `Encoded`, `Normalized` and `DecoderError`:
  everything the routine returned (data together with errors, keys
  beside data, empty lists).
- `Uplink.OmitRecvTime`.
- `Limits.Seccomp` and `Limits.Namespaces` for worker processes on Linux
  (pure Go: a seccomp deny-list filter; user, network, mount, IPC and UTS
  namespaces).
- `Codec.Usage`: the workers' peak resident size, CPU time and calls.
- `devicerepo`: finds a device's codec by vendor, device, firmware and
  region in a copy of the lorawan-devices repository.
- Interop: every codec of lorawan-devices with examples (729 codec files,
  4270 examples), and every example also run through The Things Stack
  v3.36.2's runner in Docker as the reference.

Changed:

- Every call runs in a fresh runtime, as in The Things Stack; a script's
  globals no longer carry over (`Limits.KeepState` restores reuse).
- Typed array constructors are wrapped in plain functions instead of
  proxies, so `x instanceof Int16Array` works again in scripts (it threw).

## v0.1.1 (2026-10-11)

- The repository now matches what v0.1.0 was meant to be. v0.1.0 carried
  copies of TheThingsNetwork/lorawan-devices codec files under
  `ttn/testdata/lorawan-devices`, whose repository has no licence file of its
  own; the test module now fetches them at test time from a pinned commit
  and checks each against its SHA-256 (`ttn/testdata/files.sha256`), so none
  ship. A stray copy of the module under `jscodec/` is gone too.
- v0.1.0 is retracted in go.mod for that reason. Its code is the same.

## v0.1.0 (2026-10-11)

- First release: a sandbox for TTN-style JavaScript payload codecs
  (`decodeUplink`, `encodeDownlink`, `decodeDownlink`, `normalizeUplink`,
  `frameLength`) on goja, with time, stack, memory and size limits and
  optional worker processes.
