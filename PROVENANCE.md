# Provenance

## How the code was written

Written for SigmaM2M, a oneM2M platform, in Go, by the author of the cim
module and from the same documents. No code was ported or copied from
another implementation; no open IEC 61968 head-end system was found.

The simulator was first written next to SigmaM2M's CIM adapter to test it
without a head-end system, moved into the cim module on 2026-10-06 and
out of it into this module on 2026-10-11 (with `git mv`, so history
follows sim/sim.go and cmd/cimsim), so the library does not depend on
its simulator.

For the first release it gained the generic noun store, the edition
handling of get answers, the event target and the fault switches
(sim/generic.go), and tests driving every noun and every UCAIug example
through it. An independent review, made from public material only,
listed the faults a HES simulator should produce (error replies with
codes, partial results, SOAP faults and FaultMessages, timeouts,
outages, asynchronous replies that never come); the switches cover
those, and README lists what is not simulated.

How the HES behaves where the documents leave it open (keeping items by
mRID or first name, refusing a duplicate create, answering a get with
everything it holds) is our own choice.

## Sources

| Area (files) | Followed |
|---|---|
| Simulator (sim/) | IEC 61968-100 and IEC 61968-9, through the cim module |
| Command (cmd/cimsim) | none (flags and demo meters are our own) |
| Test vectors (testdata/) | the UCAIug example messages, unmodified |

No specification text is reproduced.

## Tests

| Test | Source |
|---|---|
| hes_test.go, cmd/cimsim | messages built by the cim library against this simulator, written from the same documents; not a real head-end system |
| hes_test.go (examples) | the UCAIug example messages and schemas |
