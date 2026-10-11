# cim-sim

A simulated head-end system (HES) speaking IEC 61968-100 messages with IEC 61968-9 payloads, for testing meter data management systems and other CIM clients without a HES; not affiliated with the IEC or the UCA International Users Group.

[![Go Reference](https://pkg.go.dev/badge/github.com/cB-Sigma/cim-sim.svg)](https://pkg.go.dev/github.com/cB-Sigma/cim-sim) [![test](https://github.com/cB-Sigma/cim-sim/actions/workflows/test.yml/badge.svg)](https://github.com/cB-Sigma/cim-sim/actions/workflows/test.yml) [![Licence: Apache-2.0](https://img.shields.io/badge/licence-Apache--2.0-blue.svg)](LICENSE) [![Go](https://img.shields.io/badge/go-1.25-00ADD8.svg)](go.mod)

The HES serves SOAP on `/cim` (and, optionally, a message broker the
caller connects), answers in the envelope edition of each request (2013
or 2022) and in the payload edition it uses (2013 or 2024), and can be
made to fail in every way a client must survive. It is built on
[cim](https://github.com/cB-Sigma/cim). Written for SigmaM2M, a oneM2M
platform; it has no dependency on it.

## What is simulated

| Feature | Status |
|---|---|
| get MeterReadings from readings the test sets (`SetReadings`), get MeterConfig from meters it sets (`SetMeters`) | yes |
| create EndDeviceControls: carried out, failed (`FailControls`) or partly failed for one meter (`SetPartialControls`), with transaction IDs in 2022 for a later cancel | yes |
| Every other noun of `cim.Nouns` in both IEC 61968-9 editions: create, change, delete, cancel, execute (and close, in 2013 envelopes) kept in a store by mRID or name, get answered in the request's edition, unknown objects and duplicates refused with the edition's reply codes, 2024 payloads checked against the UCAIug schemas (2013 payloads have no open schema and pass) | yes |
| Events posted to the HES (created, changed ...) checked like requests and acknowledged | yes |
| Each change it accepts reported as an event (created, changed, deleted, canceled, executed, closed) to an event target (`SetEventTarget`) | yes |
| Asynchronous replies to the reply address after an acknowledgement (`SetAsync`), an extra acknowledgement first (`SetAckFirst`), no reply at all (silent), the request's CorrelationID echoed (`SetEchoCorrelation`) | yes |
| A message broker the caller connects (`ConnectBroker`, `PublishEvent`) | yes |
| Faults: SOAP faults (`SetSOAPFault`), FaultMessages in the request's envelope edition (`SetFaultMessage`), HTML error pages (`SetHTTPError`), failed replies with a chosen code for one request type or all (`SetReplyError`), accepted-and-queued replies with no result, code 0.4 (`SetQueued`), a CorrelationID matching no request (`SetWrongCorrelation`), replies cut off or in the wrong namespaces (`SetMalformedReplies`), 503 (`SetDown`), slow answers (`SetDelay`), no answer (`SetHang`), a soap:Client fault for a malformed request | yes |
| Filters of get requests (time ranges, reading types), answers spread over several messages, duplicate or out-of-order asynchronous replies, OperationSet execution, JMS (use `ConnectBroker` with your own client), real meter behaviour | no |

## Install

```
go get github.com/cB-Sigma/cim-sim@latest
```

Requires Go 1.25 or later. The command:

```
go install github.com/cB-Sigma/cim-sim/cmd/cimsim@latest
```

## Command line

```
cimsim -listen 127.0.0.1:8093
```

| Flag | Default | Meaning |
|---|---|---|
| `-listen` | `127.0.0.1:8093` | address to listen on; SOAP is at `/cim` |

The runner serves the HES with demo meters and readings.

## In Go tests

```go
h := sim.New()
defer h.Close()
h.SetReadings("MTR-1", cim.Reading{Value: "1234.5", ReadingType: cim.Ref{Ref: "0.0.0.1.1.1.12.0.0.0.0.0.0.0.0.3.72.0"}})
req := &cim.Message{Kind: cim.KindRequest, Version: cim.Version2022,
	Header: cim.NewHeader("get", "MeterReadings", "my-mdm", time.Now(), id)}
status, reply, err := sim.Post(h.URL(), req, "", "")
h.SetReplyError("create EndDeviceControls", cim.ReplyError{Code: "2.4", Reason: "meter busy"})
```

## Documentation

Reference: https://pkg.go.dev/github.com/cB-Sigma/cim-sim

| Package | What it does |
|---|---|
| `sim` | `New`, `NewAt`, `HES` (state setters and readers, fault switches, `Stored`, `EventsSent`, `Requests`, `Raw`, `Auth`, `ConnectBroker`, `PublishEvent`), `Post`, `PostRaw`, `Event`, `Payload` |
| `cmd/cimsim` | serves the HES |

Changes per version are in [CHANGELOG.md](CHANGELOG.md).

## Testing

```
go test -race ./...
```

The tests at the module root drive the HES with messages built by the
cim library: every noun of the noun table in each of its editions and
both envelope editions (create, read back valid, change, delete, cancel,
close in 2013, events), every UCAIug example message, and every fault
switch.

### Interop tested against

- Simulator only: no HES or MDM of another maker is openly available.
  The simulator and the cim library are written by the same author from
  the same documents, so their agreement is not independent.
- Outside evidence: the UCAIug IEC 61968-9 3rd edition (CDV2) schemas
  and example messages (Apache-2.0). Every payload the HES accepts is
  checked against the UCAIug schema of its noun; every example message
  (rebound to the 2024 namespaces) was sent to it in 2013 and 2022
  envelopes on 2026-10-11: the 11 valid ones were accepted (those of
  nouns the generic store keeps were read back with the same values),
  figure 26 (a MeterReadings event the schema refuses) was refused, and
  figure 61 (a MeterConfig the schema refuses) was accepted, because
  create MeterConfig is accepted without being checked or kept. The get
  answers of every noun with a schema were valid against it.
- An independent review of this README against the public material
  (two rounds, before the release; the reviewer did not read the code)
  listed missing faults and misread verbs and editions; they were
  added or corrected, and what is not simulated is listed above.

## Demo values

Every mRID, meter name, reading and address in this module is a made-up
demo value, apart from the UCAIug examples in `testdata/`.

## Related repositories

- [cim](https://github.com/cB-Sigma/cim): the IEC 61968 message library
  this simulator is built on.

## Status

v0.x: the API may change before v1.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Security

See [SECURITY.md](SECURITY.md).

## Licence

Apache-2.0 (see [LICENSE](LICENSE) and [NOTICE](NOTICE)). Copyright 2026
Abhinav Gautam. IEC is a trademark of the International Electrotechnical
Commission and UCA a trademark of the UCA International Users Group; the
names are used only to say which standards this module simulates.
