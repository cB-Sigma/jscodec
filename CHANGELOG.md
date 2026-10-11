# Changelog

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
