// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Command cimsim serves a simulated head-end system (HES) that answers
// IEC 61968-100 messages over SOAP: get MeterReadings, get MeterConfig and
// create EndDeviceControls, with two demo meters, so a CIM client can be
// tried without a HES. For example:
//
//	cimsim -listen 127.0.0.1:8093
//
// The endpoint is /cim.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cB-Sigma/cim"
	"github.com/cB-Sigma/cim-sim/sim"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, nil); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "cimsim:", err)
		os.Exit(1)
	}
}

// DemoMeters are the mRIDs of the meters the HES knows.
var DemoMeters = []string{"6c5b0000-0000-4000-8000-00000000a001", "6c5b0000-0000-4000-8000-00000000a002"}

// EnergyImport is the ReadingType of the demo readings: forward active
// energy, kWh (IEC 61968-9 annex C).
const EnergyImport = "0.0.0.1.1.1.12.0.0.0.0.0.0.0.0.3.72.0"

// run serves until ctx ends; ready, when not nil, receives the HES.
func run(ctx context.Context, args []string, out io.Writer, ready chan<- *sim.HES) error {
	fs := flag.NewFlagSet("cimsim", flag.ContinueOnError)
	fs.SetOutput(out)
	listen := fs.String("listen", "127.0.0.1:8093", "address to listen on")
	if err := fs.Parse(args); err != nil {
		return err
	}
	h, err := sim.NewAt(*listen)
	if err != nil {
		return err
	}
	defer h.Close()
	now := time.Now().UTC().Truncate(15 * time.Minute)
	var meters []cim.ConfigDevice
	for i, id := range DemoMeters {
		meters = append(meters, cim.ConfigDevice{MRID: id, Names: []cim.Name{{Name: fmt.Sprintf("DEMO%04d", i+1)}}})
		var rs []cim.Reading
		for k := 3; k >= 0; k-- {
			rs = append(rs, cim.Reading{TimeStamp: now.Add(-time.Duration(k) * 15 * time.Minute).Format(time.RFC3339),
				Value: fmt.Sprintf("%d.%d", 1000*(i+1)+(3-k), 5), ReadingType: cim.Ref{Ref: EnergyImport}})
		}
		h.SetReadings(id, rs...)
	}
	h.SetMeters(meters...)
	fmt.Fprintf(out, "cimsim HES at %s\n", h.URL())
	if ready != nil {
		ready <- h
	}
	<-ctx.Done()
	return nil
}
