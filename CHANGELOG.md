# Changelog

All notable changes to this module. Versions follow semantic versioning;
before v1 the API may change in minor versions.

## v0.1.0 (2026-10-11)

First release as a module of its own, split from the cim module: the
simulated head-end system (`sim`) and `cmd/cimsim`.

- Every noun of `cim.Nouns` is handled: a generic store for create,
  change, delete, cancel, close and execute, get answered in the payload
  edition of the request, payloads checked against the UCAIug schemas,
  duplicates and unknown objects refused with the edition's codes.
- get MeterReadings and get MeterConfig are answered in the payload
  edition of the request (before, always in the 2011 namespaces).
- Events posted to the HES are checked and acknowledged; accepted
  changes are reported to an event target (`SetEventTarget`).
- Faults: `SetSOAPFault`, `SetFaultMessage` (2013 or 2022, as the
  request), `SetHTTPError`, `SetReplyError`, `SetQueued`,
  `SetWrongCorrelation`, `SetMalformedReplies`, `SetDelay`, `SetHang`,
  next to `SetDown`,
  `FailControls`, `SetPartialControls` and the asynchronous switches.
- Tests: every noun in each of its editions and both envelope editions,
  every UCAIug example message, every fault.
