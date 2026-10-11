// Package sim is a fake head-end system (HES) speaking IEC 61968-100
// messages (2013 and 2022 envelopes) with IEC 61968-9 payloads (2013 and
// 2024), over SOAP (an HTTP server) and over a message broker the caller
// connects (Broker), for tests of CIM clients and servers. It answers
// get MeterReadings, get MeterConfig and create EndDeviceControls with
// its own logic, keeps every other noun in a generic store (create,
// change, delete, cancel, close and execute, then get, in the edition
// the request uses, with payloads checked against the UCAIug schemas),
// reports changes as events, answers asynchronously when asked, and
// plays faults: SOAP faults, FaultMessages, HTML error pages, failed and
// partial replies with chosen codes, outages, slow and hanging answers.
package sim

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/cB-Sigma/cim"
)

// HES is the fake head-end system.
type HES struct {
	srv *httptest.Server

	mu       sync.Mutex
	readings map[string][]cim.Reading // mRID → readings the HES returns to a get
	meters   []cim.ConfigDevice
	requests []*cim.Message
	raw      [][]byte // SOAP envelopes as received
	auth     [][2]string
	// Down makes the SOAP endpoint answer 503.
	down bool
	// FailControls makes EndDeviceControls replies FAILED with this text.
	failControls string
	// partialControls makes EndDeviceControls replies PARTIAL: the
	// control of this meter mRID failed, the others were carried out.
	partialControls string
	// Async answers requests that ask for it with an AcknowledgementWith and a
	// later reply to ReplyAddress.
	async bool
	// silent never sends the asynchronous reply.
	silent bool
	// echoCorrelation sets each reply's CorrelationID to the request's
	// CorrelationID (as some ESBs do) instead of its MessageID.
	echoCorrelation bool
	// ackFirst sends an AcknowledgementWith to the reply address before the
	// asynchronous reply (an ESB acknowledging receipt on the callback).
	ackFirst bool
	conn     Broker
	// store holds the items of every noun handled generically (see
	// generic.go), by noun.
	store map[string][]*cim.Element
	f     faultState
}

// Broker is the HES's connection to a message broker (MQTT, say), made by
// the caller: it publishes, and is closed with the HES.
type Broker interface {
	Publish(ctx context.Context, topic string, payload []byte, retain bool) error
	Close()
}

// New starts the SOAP endpoint on a loopback port.
func New() *HES {
	h := &HES{readings: map[string][]cim.Reading{}}
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	return h
}

// NewAt starts the SOAP endpoint listening on addr.
func NewAt(addr string) (*HES, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	h := &HES{readings: map[string][]cim.Reading{}}
	h.srv = httptest.NewUnstartedServer(http.HandlerFunc(h.serve))
	_ = h.srv.Listener.Close()
	h.srv.Listener = ln
	h.srv.Start()
	return h, nil
}

// URL is the SOAP endpoint.
func (h *HES) URL() string { return h.srv.URL + "/cim" }

// Close stops the servers.
func (h *HES) Close() {
	h.srv.Close()
	h.mu.Lock()
	c := h.conn
	h.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// SetPartialControls makes create EndDeviceControls answer PARTIAL, with
// the control of meter mrid failed ("" answers normally again).
func (h *HES) SetPartialControls(mrid string) {
	h.mu.Lock()
	h.partialControls = mrid
	h.mu.Unlock()
}

// SetReadings sets what a get MeterReadings returns for a meter.
func (h *HES) SetReadings(mrid string, rs ...cim.Reading) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.readings[mrid] = rs
}

// SetMeters sets what a get MeterConfig returns.
func (h *HES) SetMeters(ms ...cim.ConfigDevice) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.meters = ms
}

// SetDown makes the SOAP endpoint fail (or recover).
func (h *HES) SetDown(v bool) { h.mu.Lock(); h.down = v; h.mu.Unlock() }

// FailControls makes controls fail with reason ("" to succeed).
func (h *HES) FailControls(reason string) { h.mu.Lock(); h.failControls = reason; h.mu.Unlock() }

// SetAsync answers asynchronously when asked; silent never replies.
func (h *HES) SetAsync(async, silent bool) {
	h.mu.Lock()
	h.async, h.silent = async, silent
	h.mu.Unlock()
}

// SetEchoCorrelation makes replies carry the request's CorrelationID.
func (h *HES) SetEchoCorrelation(v bool) { h.mu.Lock(); h.echoCorrelation = v; h.mu.Unlock() }

// SetAckFirst makes asynchronous replies follow an AcknowledgementWith sent
// to the same reply address.
func (h *HES) SetAckFirst(v bool) { h.mu.Lock(); h.ackFirst = v; h.mu.Unlock() }

// Requests are the requests received so far.
func (h *HES) Requests() []*cim.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*cim.Message(nil), h.requests...)
}

// Auth are the basic-auth credentials of each SOAP request.
// Raw returns the SOAP envelopes received, as sent.
func (h *HES) Raw() [][]byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][]byte(nil), h.raw...)
}

func (h *HES) Auth() [][2]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][2]string(nil), h.auth...)
}

func (h *HES) serve(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	down := h.down
	u, p, _ := r.BasicAuth()
	h.auth = append(h.auth, [2]string{u, p})
	h.mu.Unlock()
	if down {
		http.Error(w, "maintenance", http.StatusServiceUnavailable)
		return
	}
	b, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	h.raw = append(h.raw, b)
	h.mu.Unlock()
	m, err := cim.Parse(b)
	if err == nil {
		err = m.Decompress()
	}
	if err != nil {
		writeSOAPFault(w, "soap:Client", err.Error())
		return
	}
	if h.faultAnswer(w, r, m) {
		h.mu.Lock()
		h.requests = append(h.requests, m)
		h.mu.Unlock()
		return
	}
	resp := h.answer(m, func(reply *cim.Message) {
		body, _ := reply.MarshalSOAP()
		req, _ := http.NewRequest(http.MethodPost, m.Header.ReplyAddress, bytes.NewReader(body))
		req.Header.Set("Content-Type", "text/xml; charset=utf-8")
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
		}
	})
	out, _ := resp.MarshalSOAP()
	h.mu.Lock()
	malformed := h.f.malformed
	h.mu.Unlock()
	switch malformed {
	case "xml":
		out = out[:len(out)/2] // cut off mid-message
	case "namespace":
		out = bytes.ReplaceAll(out, []byte(cim.MessageNS2022), []byte(cim.MessageNS))
		out = bytes.ReplaceAll(out, []byte(cim.NSBase2024), []byte(cim.NSBase))
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_, _ = w.Write(out)
}

// answer computes the reply to a request; with asynchronous replies it
// returns an AcknowledgementWith and sends the reply with later.
func (h *HES) answer(m *cim.Message, later func(*cim.Message)) *cim.Message {
	h.mu.Lock()
	h.requests = append(h.requests, m)
	// 2013 asks with AsyncReplyFlag; a 2022 request names a reply address.
	async := h.async && m.Header.ReplyAddress != "" && (m.Version == cim.Version2022 || (m.Header.AsyncReplyFlag != nil && *m.Header.AsyncReplyFlag))
	silent, ackFirst, echo := h.silent, h.ackFirst, h.echoCorrelation
	h.mu.Unlock()
	reply := h.reply(m)
	h.mu.Lock()
	wrongCorr, queued := h.f.wrongCorrelation, h.f.queued
	h.mu.Unlock()
	if queued && reply.Reply != nil && reply.Reply.Result == cim.ResultOK {
		// Accepted and queued (code 0.4): no result in this answer.
		level := "INFORM"
		if m.Version == cim.Version2022 {
			level = "OK"
		}
		reply.Payload = nil
		reply.Reply.Errors = []cim.ReplyError{{Code: "0.4", Level: level, Reason: "accepted and queued"}}
	}
	if wrongCorr {
		reply.Header.CorrelationID = "6f1c0000-0000-4000-8000-0000000bad00"
	}
	if echo {
		reply.Header.CorrelationID = m.Header.CorrelationID
	}
	if !async {
		return reply
	}
	if !silent {
		go func() {
			if ackFirst {
				time.Sleep(20 * time.Millisecond)
				later(cim.Acknowledgement(m, "fake-hes", time.Now(), "ack2-"+m.Header.MessageID))
			}
			time.Sleep(50 * time.Millisecond)
			later(reply)
		}()
	}
	return cim.Acknowledgement(m, "fake-hes", time.Now(), "ack-"+m.Header.MessageID)
}

func (h *HES) reply(m *cim.Message) *cim.Message {
	ok := cim.Response(m, "fake-hes", time.Now(), "r-"+m.Header.MessageID, nil)
	fail := func(err error) *cim.Message {
		return cim.Response(m, "fake-hes", time.Now(), "r-"+m.Header.MessageID, err)
	}
	h.mu.Lock()
	e, refuse := h.f.replyErrors[m.Header.Verb+" "+m.Header.Noun]
	if !refuse {
		e, refuse = h.f.replyErrors["*"]
	}
	h.mu.Unlock()
	if refuse {
		return failWith(m, e)
	}
	switch m.Header.Verb + " " + m.Header.Noun {
	case "get MeterReadings":
		var g cim.GetMeterReadings
		if err := xml.Unmarshal(m.Request, &g); err != nil {
			return fail(err)
		}
		h.mu.Lock()
		out := cim.MeterReadings{}
		ids := g.EndDevices
		if len(ids) == 0 {
			for id := range h.readings {
				ids = append(ids, cim.Device{MRID: id})
			}
		}
		for _, d := range ids {
			if rs, ok := h.readings[d.MRID]; ok {
				out.MeterReadings = append(out.MeterReadings, cim.MeterReading{Meter: cim.Device{MRID: d.MRID}, Readings: rs})
			}
		}
		h.mu.Unlock()
		ok.Payload, _ = cim.PayloadXML(editionOf(m), "MeterReadings", out)
	case "get MeterConfig":
		h.mu.Lock()
		out := cim.MeterConfig{Meters: h.meters}
		h.mu.Unlock()
		ok.Payload, _ = cim.PayloadXML(editionOf(m), "MeterConfig", out)
	case "create EndDeviceControls":
		var c cim.EndDeviceControls
		if err := xml.Unmarshal(m.Payload, &c); err != nil || len(c.Controls) == 0 {
			return fail(errors.New("no EndDeviceControl in the payload"))
		}
		h.mu.Lock()
		reason := h.failControls
		h.mu.Unlock()
		if reason != "" {
			return fail(errors.New(reason))
		}
		h.mu.Lock()
		partial := h.partialControls
		h.mu.Unlock()
		if partial != "" {
			level := "FATAL"
			if m.Version == cim.Version2022 {
				level = "ERROR"
			}
			okLevel := "INFORM"
			if m.Version == cim.Version2022 {
				okLevel = "OK"
			}
			ok.Reply.Result = cim.ResultPartial
			ok.Reply.Errors = nil
			for _, ctl := range c.Controls {
				for _, ed := range ctl.EndDevices {
					if ed.MRID != partial {
						ok.Reply.Errors = append(ok.Reply.Errors, cim.ReplyError{Code: "0.0", Level: okLevel, ID: &cim.ErrorID{Value: ed.MRID, ObjectType: "EndDevice"}})
					}
				}
			}
			ok.Reply.Errors = append(ok.Reply.Errors, cim.ReplyError{Code: "2.12", Level: level, Details: "the meter did not answer the control",
				ID: &cim.ErrorID{Value: partial, ObjectType: "EndDevice"}})
		}
		if m.Version == cim.Version2022 {
			// A transaction ID per control, for a later cancel.
			for _, c := range c.Controls {
				ok.Reply.IDs = append(ok.Reply.IDs, "7d3ee1d0-0000-4000-8000-"+fmt.Sprintf("%012d", len(c.MRID)))
			}
		}
	case "change MeterConfig", "create MeterConfig", "delete MeterConfig", "change EndDeviceConfig", "create MeterServiceRequests",
		"change UsagePointConfig", "create MasterDataLinkageConfig", "cancel EndDeviceControls", "cancel MeterServiceRequests",
		"created Incidents", "changed Incidents", "created UnplannedOutages", "changed UnplannedOutages":
		// Accepted (recorded in Requests).
	default:
		if err := h.generic(m, ok); err != nil {
			return fail(err)
		}
	}
	return ok
}

// Payload marshals v as a payload root in its noun's namespace.
func Payload(noun string, v any) ([]byte, error) {
	b, err := xml.Marshal(v)
	if err != nil {
		return nil, err
	}
	open := []byte("<" + noun)
	if !bytes.HasPrefix(b, open) {
		return nil, fmt.Errorf("root is not %s", noun)
	}
	return append([]byte(`<`+noun+` xmlns="`+cim.NSBase+noun+`#"`), b[len(open):]...), nil
}

// Event builds an EventMessage.
func Event(verb, noun string, payload []byte) *cim.Message {
	return &cim.Message{Kind: cim.KindEvent, Header: cim.Header{
		Verb: verb, Noun: noun, Revision: "2.0", Timestamp: time.Now().UTC().Format(time.RFC3339), Source: "fake-hes",
		MessageID: fmt.Sprintf("ev-%d", time.Now().UnixNano()),
	}, Payload: payload}
}

// Post sends a message over SOAP to target (the IPE's receiver) with
// optional basic auth, and returns the HTTP status and parsed reply.
func Post(target string, m *cim.Message, user, pass string) (int, *cim.Message, error) {
	body, err := m.MarshalSOAP()
	if err != nil {
		return 0, nil, err
	}
	return PostRaw(target, body, user, pass)
}

// PostRaw sends a SOAP envelope as it is (for example one secured with
// WS-Security) and returns the HTTP status and parsed reply.
func PostRaw(target string, body []byte, user, pass string) (int, *cim.Message, error) {
	req, _ := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	reply, err := cim.Parse(b)
	return res.StatusCode, reply, err
}

// ConnectBroker makes the HES serve requests from requestTopic over a
// broker: dial connects to the broker, subscribes to requestTopic and
// passes each message's payload to handle. Each request is answered on its
// ReplyAddress.
func (h *HES) ConnectBroker(ctx context.Context, requestTopic string,
	dial func(ctx context.Context, topic string, handle func(payload []byte)) (Broker, error)) error {
	var c Broker
	var err error
	c, err = dial(ctx, requestTopic, func(payload []byte) {
		m, err := cim.Parse(payload)
		if err != nil || m.Header.ReplyAddress == "" {
			return
		}
		publish := func(reply *cim.Message) {
			b, _ := reply.Marshal()
			_ = c.Publish(context.Background(), m.Header.ReplyAddress, b, false)
		}
		h.mu.Lock()
		silent := h.silent
		h.requests = append(h.requests, m)
		h.mu.Unlock()
		if !silent {
			go publish(h.reply(m))
		}
	})
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.conn = c
	h.mu.Unlock()
	return nil
}

// PublishEvent publishes an EventMessage on topic.
func (h *HES) PublishEvent(ctx context.Context, topic string, m *cim.Message) error {
	h.mu.Lock()
	c := h.conn
	h.mu.Unlock()
	if c == nil {
		return errors.New("the fake HES is not on the broker")
	}
	b, err := m.Marshal()
	if err != nil {
		return err
	}
	return c.Publish(ctx, topic, b, false)
}
