// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cB-Sigma/cim"
)

// Fault switches and the generic noun store: every noun of cim.Nouns
// (and of any other profile) can be created, changed, deleted, canceled,
// closed, executed and read back, in the edition the request uses.

// faultState is the HES's fault configuration.
type faultState struct {
	soapFault   [2]string // faultcode, faultstring; "" for none
	faultMsg    string    // reason of a 2022 FaultMessage; "" for none
	httpStatus  int       // an HTML error page with this status
	delay       time.Duration
	hang        bool
	replyErrors map[string]cim.ReplyError // "verb Noun" or "*" → failed reply
	eventTarget string
	events      int

	wrongCorrelation bool
	queued           bool
	malformed        string
}

// SetWrongCorrelation makes replies carry a CorrelationID that matches no
// request.
func (h *HES) SetWrongCorrelation(v bool) { h.mu.Lock(); h.f.wrongCorrelation = v; h.mu.Unlock() }

// SetQueued makes successful replies say "accepted and queued" (code 0.4)
// with no result.
func (h *HES) SetQueued(v bool) { h.mu.Lock(); h.f.queued = v; h.mu.Unlock() }

// SetMalformedReplies makes synchronous replies broken: "xml" cuts them
// off mid-message, "namespace" moves a 2022 envelope and 2024 payloads to
// the 2011 namespaces; "" turns it off.
func (h *HES) SetMalformedReplies(kind string) { h.mu.Lock(); h.f.malformed = kind; h.mu.Unlock() }

// SetSOAPFault makes every request answer HTTP 500 with a SOAP 1.1 fault
// (code "soap:Client" or "soap:Server"); empty code turns it off.
func (h *HES) SetSOAPFault(code, text string) {
	h.mu.Lock()
	h.f.soapFault = [2]string{code, text}
	h.mu.Unlock()
}

// SetFaultMessage makes every request answer an IEC 61968-100:2022
// FaultMessage (in the request's envelope edition) with code 5.1 and this
// reason; "" turns it off.
func (h *HES) SetFaultMessage(reason string) { h.mu.Lock(); h.f.faultMsg = reason; h.mu.Unlock() }

// SetHTTPError makes every request answer an HTML page with status (a
// proxy's 502, a 401 login page); 0 turns it off.
func (h *HES) SetHTTPError(status int) { h.mu.Lock(); h.f.httpStatus = status; h.mu.Unlock() }

// SetDelay delays every answer by d; SetHang never answers.
func (h *HES) SetDelay(d time.Duration) { h.mu.Lock(); h.f.delay = d; h.mu.Unlock() }

// SetHang makes the HES accept requests and never answer them (until the
// client gives up).
func (h *HES) SetHang(v bool) { h.mu.Lock(); h.f.hang = v; h.mu.Unlock() }

// SetReplyError makes the requests of "verb Noun" ("create
// EndDeviceControls"; "*" for every request) answer a FAILED reply with
// this error (code, level, reason, details as given); a zero error turns
// it off.
func (h *HES) SetReplyError(verbNoun string, e cim.ReplyError) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.f.replyErrors == nil {
		h.f.replyErrors = map[string]cim.ReplyError{}
	}
	if e.Code == "" {
		delete(h.f.replyErrors, verbNoun)
		return
	}
	h.f.replyErrors[verbNoun] = e
}

// SetEventTarget makes the HES report each change it accepts (create,
// change, delete, cancel, close, execute) as an event message (created,
// changed ...) posted over SOAP to url, as a HES publishing to an MDM
// does; "" turns it off.
func (h *HES) SetEventTarget(url string) { h.mu.Lock(); h.f.eventTarget = url; h.mu.Unlock() }

// EventsSent counts the event messages posted to the event target.
func (h *HES) EventsSent() int { h.mu.Lock(); defer h.mu.Unlock(); return h.f.events }

// Stored returns the payload the HES holds for a noun, in the edition's
// namespace (an empty root when it holds none).
func (h *HES) Stored(noun string, ed cim.Edition) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.storedLocked(noun, ed)
}

func (h *HES) storedLocked(noun string, ed cim.Edition) []byte {
	root := &cim.Element{Name: noun, Children: h.store[noun]}
	return root.XML(ed.PayloadNS(noun))
}

// itemKey identifies a stored item: its mRID, else its first name, else
// its whole content.
func itemKey(e *cim.Element) string {
	if c := e.Child("mRID"); c != nil && c.Text != "" {
		return "mRID:" + c.Text
	}
	if n := e.Child("Names"); n != nil {
		if c := n.Child("name"); c != nil {
			return "name:" + c.Text
		}
	}
	return "all:" + strings.Join(e.Leaves(), "|")
}

// editionOf is the IEC 61968-9 edition a message's payload or request
// uses (2024 for a 2022 envelope without either).
func editionOf(m *cim.Message) cim.Edition {
	for _, b := range [][]byte{m.Payload, m.Request} {
		if ns := cim.RootNS(b); ns != "" {
			if strings.HasPrefix(ns, cim.NSBase2024) {
				return cim.Edition2024
			}
			return cim.Edition2013
		}
	}
	if m.Version == cim.Version2022 {
		return cim.Edition2024
	}
	return cim.Edition2013
}

var pastTense = map[string]string{"create": "created", "change": "changed", "delete": "deleted", "cancel": "canceled", "close": "closed", "execute": "executed"}

// generic answers a request of any noun with the store (called without
// h.mu held); ok is the success response it fills.
func (h *HES) generic(m *cim.Message, ok *cim.Message) error {
	verb, noun := strings.ToLower(m.Header.Verb), m.Header.Noun
	ed := editionOf(m)
	if verb == "get" {
		h.mu.Lock()
		ok.Payload = h.storedLocked(noun, ed)
		h.mu.Unlock()
		return nil
	}
	past, known := pastTense[verb]
	isEvent := cim.OneOf(verb, cim.EventVerbs...)
	if !known && !isEvent {
		return cim.Coded(cim.ClassUnsupportedVerb, "unsupported verb %s", verb)
	}
	var items []*cim.Element
	if len(bytes.TrimSpace(m.Payload)) > 0 {
		root, err := cim.ParseElement(m.Payload)
		if err != nil {
			return cim.Coded(cim.ClassInvalid, "%v", err)
		}
		if root.Name != noun {
			return cim.Coded(cim.ClassInvalid, "the payload is %s, the header says %s", root.Name, noun)
		}
		if err := cim.CheckPayload(m.Payload); err != nil {
			return cim.Coded(cim.ClassInvalid, "%v", err)
		}
		items = root.Children
	}
	if isEvent {
		return nil // an event posted to the HES, checked: acknowledged
	}
	h.mu.Lock()
	if h.store == nil {
		h.store = map[string][]*cim.Element{}
	}
	list := h.store[noun]
	switch verb {
	case "create":
		for _, it := range items {
			for _, have := range list {
				if itemKey(have) == itemKey(it) && !strings.HasPrefix(itemKey(it), "all:") {
					h.mu.Unlock()
					return cim.Coded(cim.ClassIDInUse, "%s already exists", itemKey(it))
				}
			}
		}
		list = append(list, items...)
	case "change":
		for _, it := range items {
			found := false
			for i, have := range list {
				if itemKey(have) == itemKey(it) {
					list[i], found = it, true
				}
			}
			if !found {
				h.mu.Unlock()
				return cim.Coded(cim.ClassUnknownObject, "%s is not known", itemKey(it))
			}
		}
	case "delete", "cancel", "close":
		for _, it := range items {
			k := itemKey(it)
			out := list[:0]
			for _, have := range list {
				if itemKey(have) != k {
					out = append(out, have)
				}
			}
			list = out
		}
	}
	h.store[noun] = list
	target := h.f.eventTarget
	if target != "" {
		h.f.events++
	}
	h.mu.Unlock()
	if target != "" {
		ev := Event(past, noun, m.Payload)
		ev.Version = m.Version
		go func() { _, _, _ = Post(target, ev, "", "") }()
	}
	return nil
}

// failWith builds a FAILED response with one error.
func failWith(m *cim.Message, e cim.ReplyError) *cim.Message {
	r := cim.Response(m, "fake-hes", time.Now(), "r-"+m.Header.MessageID, errors.New(e.Reason))
	level := e.Level
	if level == "" {
		level = "FATAL"
		if m.Version == cim.Version2022 {
			level = "ERROR"
		}
	}
	e.Level = level
	if m.Version == cim.Version2022 {
		e.Edition = cim.Version2022
	}
	r.Reply = &cim.Reply{Result: cim.ResultFailed, Errors: []cim.ReplyError{e}}
	return r
}

// faultAnswer writes the answer a fault switch asks for; it reports
// whether one did.
func (h *HES) faultAnswer(w http.ResponseWriter, r *http.Request, m *cim.Message) bool {
	h.mu.Lock()
	f := h.f
	h.mu.Unlock()
	if f.hang {
		<-r.Context().Done()
		return true
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-r.Context().Done():
			return true
		}
	}
	switch {
	case f.httpStatus != 0:
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(f.httpStatus)
		fmt.Fprintf(w, "<html><body><h1>%d %s</h1></body></html>", f.httpStatus, http.StatusText(f.httpStatus))
		return true
	case f.soapFault[0] != "":
		writeSOAPFault(w, f.soapFault[0], f.soapFault[1])
		return true
	case f.faultMsg != "" && m != nil:
		resp := failWith(m, cim.ReplyError{Code: "5.1", Reason: f.faultMsg})
		var out []byte
		if m.Version == cim.Version2022 {
			resp.Root = "FaultMessage"
			out, _ = resp.MarshalSOAP()
		} else {
			// The 2013 FaultMessage holds a Reply only.
			b, _ := xml.Marshal(struct {
				XMLName xml.Name   `xml:"http://iec.ch/TC57/2011/schema/message FaultMessage"`
				Reply   *cim.Reply `xml:"Reply"`
			}{Reply: resp.Reply})
			out = append(append([]byte(`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body>`), b...), `</soap:Body></soap:Envelope>`...)
		}
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(out)
		return true
	}
	return false
}

func writeSOAPFault(w http.ResponseWriter, code, text string) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	var b bytes.Buffer
	b.WriteString(`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><soap:Fault><faultcode>`)
	_ = cim.XMLEscape(&b, code)
	b.WriteString(`</faultcode><faultstring>`)
	_ = cim.XMLEscape(&b, text)
	b.WriteString(`</faultstring></soap:Fault></soap:Body></soap:Envelope>`)
	_, _ = w.Write(b.Bytes())
}
