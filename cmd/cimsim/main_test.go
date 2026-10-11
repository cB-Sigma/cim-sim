// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"testing"
	"time"

	"github.com/cB-Sigma/cim"
	"github.com/cB-Sigma/cim-sim/sim"
)

// A get MeterReadings request built with the library is answered by the
// runner's HES with the demo readings.
func TestGetMeterReadings(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan *sim.HES, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"-listen", "127.0.0.1:0"}, &bytes.Buffer{}, ready) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	var h *sim.HES
	select {
	case h = <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("not ready")
	}
	q, err := xml.Marshal(cim.GetMeterReadings{EndDevices: []cim.Device{{MRID: DemoMeters[0]}}})
	if err != nil {
		t.Fatal(err)
	}
	req := &cim.Message{Kind: cim.KindRequest, Header: cim.Header{Verb: "get", Noun: "MeterReadings", Revision: "2.0",
		Timestamp: time.Now().UTC().Format(time.RFC3339), Source: "cimsim-test", MessageID: "m-1"}, Request: q}
	status, resp, err := sim.Post(h.URL(), req, "", "")
	if err != nil || status != 200 {
		t.Fatalf("status %d, %v", status, err)
	}
	if resp.Reply == nil || resp.Reply.Result != cim.ResultOK {
		t.Fatalf("reply = %+v", resp.Reply)
	}
	var mr cim.MeterReadings
	if err := xml.Unmarshal(resp.Payload, &mr); err != nil {
		t.Fatalf("%v\n%s", err, resp.Payload)
	}
	if len(mr.MeterReadings) != 1 || len(mr.MeterReadings[0].Readings) != 4 || mr.MeterReadings[0].Readings[0].ReadingType.Ref != EnergyImport {
		t.Errorf("readings = %+v", mr)
	}
}
