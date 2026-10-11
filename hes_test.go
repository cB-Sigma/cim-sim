// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package cim_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cB-Sigma/cim"
	"github.com/cB-Sigma/cim-sim/sim"
)

var now = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

func request(version, verb, noun string, payload []byte) *cim.Message {
	m := &cim.Message{Kind: cim.KindRequest, Version: version,
		Header: cim.NewHeader(verb, noun, "test-mdm", now, fmt.Sprintf("6f1c0000-0000-4000-8000-%012d", time.Now().UnixNano()%1e12))}
	if cim.OneOf(verb, cim.EventVerbs...) {
		m.Kind = cim.KindEvent
	}
	if verb == "get" {
		m.Request = payload
	} else {
		m.Payload = payload
	}
	return m
}

func post(t *testing.T, h *sim.HES, m *cim.Message) *cim.Message {
	t.Helper()
	status, reply, err := sim.Post(h.URL(), m, "", "")
	if err != nil || status != 200 {
		t.Fatalf("%s %s %s: %d %v", m.Version, m.Header.Verb, m.Header.Noun, status, err)
	}
	return reply
}

// handled are the verb and noun pairs the HES answers with its own logic
// (readings, meters, controls) or accepts without keeping; every other
// request goes to the generic store.
func handled(verb, noun string) bool {
	return slices.Contains([]string{"get MeterReadings", "get MeterConfig", "create EndDeviceControls", "change MeterConfig", "create MeterConfig",
		"delete MeterConfig", "change EndDeviceConfig", "create MeterServiceRequests", "change UsagePointConfig", "create MasterDataLinkageConfig",
		"cancel EndDeviceControls", "cancel MeterServiceRequests"}, verb+" "+noun)
}

// item is a payload with one item of a noun, valid against the noun's
// UCAIug schema where it has one.
func item(noun string, ed cim.Edition, mrid string) []byte {
	ns := ` xmlns="` + ed.PayloadNS(noun) + `"`
	switch noun {
	case "MeterReadings":
		return []byte(`<MeterReadings` + ns + `><MeterReading><Meter><mRID>` + mrid + `</mRID></Meter></MeterReading></MeterReadings>`)
	case "EndDeviceEvents":
		return []byte(`<EndDeviceEvents` + ns + `><EndDeviceEvent><mRID>` + mrid + `</mRID><createdDateTime>2026-10-11T12:00:00Z</createdDateTime><EndDeviceEventType ref="3.26.38.185"/></EndDeviceEvent></EndDeviceEvents>`)
	case "ComModuleConfig":
		return []byte(`<ComModuleConfig` + ns + `><ComModule><mRID>` + mrid + `</mRID><lifecycleDate/></ComModule></ComModuleConfig>`)
	case "MasterDataLinkageConfig":
		return []byte(`<MasterDataLinkageConfig` + ns + `><UsagePoint><mRID>` + mrid + `</mRID></UsagePoint></MasterDataLinkageConfig>`)
	case "EndDeviceControls":
		return []byte(`<EndDeviceControls` + ns + `><EndDeviceControl><mRID>` + mrid + `</mRID><EndDeviceControlType ref="3.31.0.23"/></EndDeviceControl></EndDeviceControls>`)
	}
	child := map[string]string{
		"MeterReadSchedules": "MeterReadSchedule", "UsagePointConfig": "UsagePoint", "UsagePointLocationConfig": "UsagePointLocation",
		"ComModuleConfig": "ComModule", "UsagePointGroups": "UsagePointGroup", "EndDeviceGroups": "EndDeviceGroup",
		"EndDeviceConfig": "EndDevice", "MeterConfig": "Meter", "EndDeviceEvents": "EndDeviceEvent",
	}[noun]
	if child == "" {
		child = strings.TrimSuffix(strings.TrimSuffix(noun, "Config"), "s")
	}
	return []byte(`<` + noun + ` xmlns="` + ed.PayloadNS(noun) + `"><` + child + `><mRID>` + mrid + `</mRID></` + child + `></` + noun + `>`)
}

// Every noun of the table, in each of its IEC 61968-9 editions and in both
// IEC 61968-100 envelope editions, is created, read back (valid against
// its UCAIug schema where it has one), changed, deleted, canceled, closed
// and executed through the simulated HES, which reports each change to an
// event target as the matching event verb.
func TestEveryNounThroughTheHES(t *testing.T) {
	h := sim.New()
	defer h.Close()
	var mu sync.Mutex
	var events []string
	rcv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if m, err := cim.Parse(b); err == nil {
			mu.Lock()
			events = append(events, m.Version+" "+m.Header.Verb+" "+m.Header.Noun)
			mu.Unlock()
			out, _ := cim.Acknowledgement(m, "mdm", now, "ack").MarshalSOAP()
			_, _ = w.Write(out)
		}
	}))
	defer rcv.Close()
	h.SetEventTarget(rcv.URL)
	wantEvents := 0
	for _, n := range cim.Nouns {
		for _, ed := range n.Editions {
			for _, version := range []string{cim.Version2013, cim.Version2022} {
				name := fmt.Sprintf("%s %s in %s", n.Name, ed, version)
				id := strings.ToLower(n.Name) + "-" + string(ed) + "-" + version
				p := item(n.Name, ed, id)
				if n.Schema && ed == cim.Edition2024 {
					if err := cim.CheckPayload(p); err != nil {
						t.Errorf("%s: test payload not valid: %v", name, err)
						continue
					}
				}
				ran := 0
				for _, verb := range n.Verbs {
					if !cim.OneOf(verb, "create", "change", "delete", "cancel", "close", "get") || handled(verb, n.Name) {
						continue
					}
					if verb != "get" && verb != "create" && handled("create", n.Name) {
						continue // nothing kept to change
					}
					var payload []byte
					if verb != "get" {
						payload = p
					} else if n.Get != "" {
						// The get request in the edition's namespace tells
						// the HES which edition to answer in.
						payload = []byte(`<` + n.Get + ` xmlns="` + ed.PayloadNS(n.Get) + `"/>`)
					}
					if verb == "change" || verb == "delete" || verb == "cancel" || verb == "close" {
						// Something to change: make sure it exists.
						if r := post(t, h, request(version, "create", n.Name, p)); r.Err() == nil {
							wantEvents++
						}
					}
					r := post(t, h, request(version, verb, n.Name, payload))
					if r.Err() != nil {
						t.Errorf("%s %s: %v", name, verb, r.Err())
						continue
					}
					ran++
					if verb != "get" {
						wantEvents++
						continue
					}
					wantNS := ed.PayloadNS(n.Name)
					if n.Get == "" { // no request to tell: the envelope's default
						wantNS = map[string]cim.Edition{cim.Version2013: cim.Edition2013, cim.Version2022: cim.Edition2024}[version].PayloadNS(n.Name)
					}
					if cim.RootNS(r.Payload) != wantNS {
						t.Errorf("%s get: answered in %s", name, cim.RootNS(r.Payload))
					}
					if err := cim.CheckPayload(r.Payload); err != nil {
						t.Errorf("%s get: answer not valid: %v", name, err)
					}
				}
				if ran == 0 && !handled("get", n.Name) && !handled("create", n.Name) {
					t.Errorf("%s: no verb ran", name)
				}
				// Clean up so the next edition starts empty.
				_ = post(t, h, request(version, "delete", n.Name, p))
			}
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n >= wantEvents || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) < wantEvents {
		t.Errorf("%d events received, want at least %d", len(events), wantEvents)
	}
	for _, v := range []string{"created", "changed", "deleted", "canceled"} {
		if !slices.ContainsFunc(events, func(e string) bool { return strings.Contains(e, " "+v+" ") }) {
			t.Errorf("no %s event", v)
		}
	}
}

// Each UCAIug example, rebound to the 2024 namespace, goes to the HES in
// both envelope editions: payloads are created and read back as sent,
// the get request example is answered; the two examples the schemas
// refuse are refused with an invalid-payload reply where the HES checks
// them.
func TestUCAIugExamplesThroughTheHES(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "ucaiug-61968-9", "Figure [0-9]*.xml"))
	if err != nil || len(files) != 13 {
		t.Fatalf("%d examples, %v", len(files), err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := cim.Rebind(raw, cim.Edition2024)
		if err != nil {
			t.Fatal(err)
		}
		e, _ := cim.ParseElement(doc)
		noun, valid := e.Name, cim.CheckPayload(doc) == nil
		for _, version := range []string{cim.Version2013, cim.Version2022} {
			h := sim.New()
			name := filepath.Base(f) + " in " + version
			if strings.HasPrefix(noun, "Get") {
				h.SetReadings("M1")
				r := post(t, h, request(version, "get", strings.TrimPrefix(noun, "Get"), doc))
				if r.Err() != nil || r.Kind != cim.KindResponse {
					t.Errorf("%s: %v", name, r.Err())
				}
				h.Close()
				continue
			}
			verb := "create"
			if noun == "EndDeviceEvents" || noun == "MeterReadings" {
				verb = "created" // published by a HES as events; the HES acknowledges
			}
			r := post(t, h, request(version, verb, noun, doc))
			switch {
			case handled(verb, noun):
				if r.Err() != nil {
					t.Errorf("%s: %v", name, r.Err())
				}
			case valid && r.Err() != nil:
				t.Errorf("%s: refused: %v", name, r.Err())
			case !valid && r.Err() == nil:
				t.Errorf("%s: an example the schema refuses was accepted", name)
			case valid && verb == "create":
				got := post(t, h, request(version, "get", noun, nil))
				if !bytes.Equal(got.Payload, doc) {
					e2, _ := cim.ParseElement(got.Payload)
					if e2 == nil || !slices.Equal(e2.Leaves(), e.Leaves()) {
						t.Errorf("%s: read back differently:\n%s\n%s", name, got.Payload, doc)
					}
				}
			}
			h.Close()
		}
	}
}

// get MeterReadings and get MeterConfig are answered in the edition of
// the request's parameters, valid against the UCAIug schemas in 2024.
func TestReadingsAndMetersInBothEditions(t *testing.T) {
	h := sim.New()
	defer h.Close()
	h.SetReadings("M1", cim.Reading{TimeStamp: "2026-10-11T12:00:00Z", Value: "12.5", ReadingType: cim.Ref{Ref: "0.0.0.1.1.1.12.0.0.0.0.0.0.0.0.3.72.0"}})
	h.SetMeters(cim.ConfigDevice{MRID: "M1"})
	for _, ed := range []cim.Edition{cim.Edition2013, cim.Edition2024} {
		for _, v := range []string{cim.Version2013, cim.Version2022} {
			for _, noun := range []string{"MeterReadings", "MeterConfig"} {
				get := []byte(`<Get` + noun + ` xmlns="` + ed.PayloadNS("Get"+noun) + `"/>`)
				r := post(t, h, request(v, "get", noun, get))
				if r.Err() != nil || cim.RootNS(r.Payload) != ed.PayloadNS(noun) || !bytes.Contains(r.Payload, []byte("M1")) {
					t.Errorf("%s %s %s: %v %s", ed, v, noun, r.Err(), r.Payload)
				}
				if err := cim.CheckPayload(r.Payload); err != nil {
					t.Errorf("%s %s %s: %v", ed, v, noun, err)
				}
			}
		}
	}
}

// The fault switches: a SOAP fault, a 2022 FaultMessage, an HTML error
// page, a failed reply with a chosen code, an outage, a slow and a
// hanging HES, and asynchronous replies that never come.
func TestFaults(t *testing.T) {
	h := sim.New()
	defer h.Close()
	get := request(cim.Version2022, "get", "UsagePointConfig", nil)

	h.SetSOAPFault("soap:Server", "database unavailable")
	if _, _, err := sim.Post(h.URL(), get, "", ""); !errors.Is(err, cim.ErrFault) || !strings.Contains(err.Error(), "database unavailable") {
		t.Errorf("SOAP fault: %v", err)
	}
	h.SetSOAPFault("", "")

	for _, v := range []string{cim.Version2013, cim.Version2022} {
		h.SetFaultMessage("schema check failed") // a 2013 or 2022 FaultMessage, as the request
		if _, _, err := sim.Post(h.URL(), request(v, "get", "UsagePointConfig", nil), "", ""); !errors.Is(err, cim.ErrFault) || !strings.Contains(err.Error(), "schema check failed") {
			t.Errorf("FaultMessage (%s): %v", v, err)
		}
	}
	h.SetFaultMessage("")

	h.SetHTTPError(http.StatusBadGateway)
	if status, _, err := sim.Post(h.URL(), get, "", ""); status != http.StatusBadGateway || err == nil {
		t.Errorf("HTML error page: %d %v", status, err)
	}
	h.SetHTTPError(0)

	for _, v := range []string{cim.Version2013, cim.Version2022} {
		h.SetReplyError("create EndDeviceControls", cim.ReplyError{Code: "2.4", Reason: "meter busy"})
		r := post(t, h, request(v, "create", "EndDeviceControls", item("EndDeviceControls", cim.Edition2024, "c1")))
		var f *cim.ReplyFailure
		if !errors.As(r.Err(), &f) || f.Result != cim.ResultFailed || len(f.Errors) != 1 || f.Errors[0].Code != "2.4" {
			t.Errorf("reply error (%s): %v", v, r.Err())
		}
		h.SetReplyError("create EndDeviceControls", cim.ReplyError{})
		h.SetReplyError("*", cim.ReplyError{Code: "6.1", Reason: "maintenance"})
		if r := post(t, h, request(v, "get", "MeterConfig", nil)); r.Err() == nil {
			t.Errorf("reply error on every request (%s) not given", v)
		}
		h.SetReplyError("*", cim.ReplyError{})
	}

	// Replies a client must not trust: a wrong CorrelationID, queued
	// without a result, cut off, in the wrong namespace.
	h.SetWrongCorrelation(true)
	if r := post(t, h, get); r.Header.CorrelationID == get.Header.MessageID {
		t.Error("the CorrelationID was right")
	}
	h.SetWrongCorrelation(false)
	h.SetQueued(true)
	if r := post(t, h, get); r.Err() != nil || len(r.Reply.Errors) != 1 || r.Reply.Errors[0].Code != "0.4" || len(r.Payload) != 0 {
		t.Errorf("queued: %+v", r.Reply)
	}
	h.SetQueued(false)
	h.SetMalformedReplies("xml")
	if _, _, err := sim.Post(h.URL(), get, "", ""); err == nil {
		t.Error("a cut-off reply parsed")
	}
	h.SetMalformedReplies("namespace")
	gb, _ := get.MarshalSOAP()
	if res, err := http.Post(h.URL(), "text/xml", bytes.NewReader(gb)); err != nil {
		t.Fatal(err)
	} else {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if bytes.Contains(b, []byte(cim.MessageNS2022)) || !bytes.Contains(b, []byte(cim.MessageNS)) {
			t.Errorf("wrong-namespace reply: %s", b)
		}
	}
	h.SetMalformedReplies("")

	h.SetDown(true)
	if status, _, _ := sim.Post(h.URL(), get, "", ""); status != http.StatusServiceUnavailable {
		t.Errorf("down: %d", status)
	}
	h.SetDown(false)

	body, _ := get.MarshalSOAP()
	quick := &http.Client{Timeout: 100 * time.Millisecond}
	h.SetDelay(400 * time.Millisecond)
	if _, err := quick.Post(h.URL(), "text/xml", bytes.NewReader(body)); err == nil {
		t.Error("a slow HES answered within the client's timeout")
	}
	h.SetDelay(0)
	h.SetHang(true)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.URL(), bytes.NewReader(body))
	if _, err := http.DefaultClient.Do(req); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("hanging HES: %v", err)
	}
	cancel()
	h.SetHang(false)

	// Asynchronous, and silent: the acknowledgement comes, the reply not.
	var mu sync.Mutex
	replies := 0
	rcv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		replies++
		mu.Unlock()
	}))
	defer rcv.Close()
	h.SetAsync(true, true)
	async := request(cim.Version2022, "get", "MeterConfig", nil)
	async.Header.ReplyAddress = rcv.URL
	if r := post(t, h, async); !r.IsAcknowledgement() {
		t.Errorf("asynchronous get: not an acknowledgement: %+v", r.Reply)
	}
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	if replies != 0 {
		t.Errorf("a silent HES replied %d times", replies)
	}
	mu.Unlock()
	h.SetAsync(true, false)
	_ = post(t, h, async)
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := replies
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			if n == 0 {
				t.Error("no asynchronous reply")
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Malformed requests are a soap:Client fault.
	res, err := http.Post(h.URL(), "text/xml", strings.NewReader("<not-cim/>"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 500 || !bytes.Contains(b, []byte("soap:Client")) {
		t.Errorf("malformed: %d %s", res.StatusCode, b)
	}
}
