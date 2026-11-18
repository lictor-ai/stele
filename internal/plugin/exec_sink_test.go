package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func records(n int) []json.RawMessage {
	out := make([]json.RawMessage, n)
	for i := range out {
		out[i] = json.RawMessage(`{"v":1,"kind":"wrote"}`)
	}
	return out
}

func TestSinksRefuseBadEntries(t *testing.T) {
	cases := map[string]string{
		"bad name":    `{"sinks": [{"name": "Bad"}]}`,
		"built in":    `{"sinks": [{"name": "regex"}]}`,
		"twice":       `{"sinks": [{"name": "a"}, {"name": "a"}]}`,
		"timeout":     `{"sinks": [{"name": "a", "timeout": "1h"}]}`,
		"unknown key": `{"sinks": [{"name": "a", "share_brief": true}]}`,
		"too many":    `{"sinks": [` + strings.TrimSuffix(strings.Repeat(`{"name": "a"},`, 17), ",") + `]}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Sinks(steleJSON(t, content), &fakeRunner{}); !errors.Is(err, ErrSinks) {
				t.Errorf("Sinks = %v, want ErrSinks", err)
			}
		})
	}
}

func TestSinksNoRunner(t *testing.T) {
	if _, err := Sinks(steleJSON(t, `{"sinks": [{"name": "a"}]}`), nil); err == nil {
		t.Error("sinks built with no runner")
	}
	if sinks, err := Sinks(steleJSON(t, `{}`), nil); err != nil || len(sinks) != 0 {
		t.Errorf("no sinks listed = %v, %v", sinks, err)
	}
}

func TestSinksShareBriefsReserved(t *testing.T) {
	sinks, err := Sinks(steleJSON(t, `{"sinks": [{"name": "a", "share_briefs": true, "timeout": "2m"}]}`), &fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sinks) != 1 || sinks[0].Name != "a" || !sinks[0].ShareBriefs || sinks[0].timeout != 2*time.Minute {
		t.Errorf("sinks = %+v", sinks)
	}
}

// A missing sink is listed with its error, which its Push returns.
func TestSinkMissingNamesTheExecutable(t *testing.T) {
	sinks, err := Sinks(steleJSON(t, `{"sinks": [{"name": "warehouse"}]}`), &fakeRunner{findErr: ErrPluginNotFound})
	if err != nil || len(sinks) != 1 {
		t.Fatalf("Sinks = %v, %v", sinks, err)
	}
	if m := sinks[0].Missing(); !errors.Is(m, ErrPluginNotFound) || !strings.Contains(m.Error(), "stele-sink-warehouse") {
		t.Errorf("Missing = %v", m)
	}
	if _, err := sinks[0].Push(context.Background(), "p1", records(1)); !errors.Is(err, ErrPluginNotFound) {
		t.Errorf("Push = %v", err)
	}
}

func sink(t *testing.T, answer string) (*Sink, *fakeRunner) {
	t.Helper()
	r := &fakeRunner{answers: map[string]string{
		"describe": `{"protocol":1,"kind":"sink","protocols":[1],"version":"1.0.0"}`,
		"push":     answer,
	}}
	sinks, err := Sinks(steleJSON(t, `{"sinks": [{"name": "a", "config": {"path": "x"}}]}`), r)
	if err != nil {
		t.Fatal(err)
	}
	return sinks[0], r
}

func TestSinkPushAccepted(t *testing.T) {
	s, r := sink(t, `{"protocol":1,"accepted":2}`)
	a, err := s.Push(context.Background(), "p1", records(3))
	if err != nil || a != (PushAnswer{Accepted: 2}) {
		t.Fatalf("Push = %+v, %v", a, err)
	}
	req := r.got[len(r.got)-1]
	for _, want := range []string{`"op":"push"`, `"project":"p1"`, `"config":{"path":"x"}`, `"records":[{"v":1,"kind":"wrote"}`} {
		if !strings.Contains(req, want) {
			t.Errorf("request lacks %s: %s", want, req)
		}
	}
}

func TestSinkPushAcceptedOutOfRange(t *testing.T) {
	for _, answer := range []string{`{"protocol":1,"accepted":4}`, `{"protocol":1,"accepted":-1}`, `{"protocol":1}`} {
		s, _ := sink(t, answer)
		if _, err := s.Push(context.Background(), "p1", records(3)); !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: err = %v, want a protocol error", answer, err)
		}
	}
}

// A sink defers with "deferred": true and "accepted": 0; its reason is made
// one printable line (ADR-0021).
func TestSinkPushDeferred(t *testing.T) {
	for answer, want := range map[string]PushAnswer{
		`{"protocol":1,"accepted":0,"deferred":true,"reason":"sending is off in this folder"}`: {Deferred: true, Reason: "sending is off in this folder"},
		`{"protocol":1,"accepted":0,"deferred":true}`:                                          {Deferred: true},
		`{"protocol":1,"accepted":0,"deferred":true,"reason":"off\n\u001b[31mred"}`:            {Deferred: true, Reason: "off red"},
		// "deferred": false is no deferral, and "reason" alone is ignored.
		`{"protocol":1,"accepted":2,"deferred":false}`: {Accepted: 2},
		`{"protocol":1,"accepted":1,"reason":"x"}`:     {Accepted: 1},
		// "deferred": null is the same as no "deferred".
		`{"protocol":1,"accepted":2,"deferred":null}`: {Accepted: 2},
	} {
		s, _ := sink(t, answer)
		a, err := s.Push(context.Background(), "p1", records(3))
		if err != nil || a != want {
			t.Errorf("%s: Push = %+v, %v; want %+v", answer, a, err, want)
		}
	}
	s, _ := sink(t, `{"protocol":1,"accepted":0,"deferred":true,"reason":"`+strings.Repeat("é", 300)+`"}`)
	if a, err := s.Push(context.Background(), "p1", records(1)); err != nil || len(a.Reason) > maxReason+len("…") {
		t.Errorf("a long reason = %d bytes, %v", len(a.Reason), err)
	}
}

// A cut reason ends in "…" with no space before it.
func TestSinkPushDeferredReasonCut(t *testing.T) {
	reason := strings.Repeat("x", maxReason-1) + "   tail"
	s, _ := sink(t, `{"protocol":1,"accepted":0,"deferred":true,"reason":"`+reason+`"}`)
	a, err := s.Push(context.Background(), "p1", records(1))
	if want := strings.Repeat("x", maxReason-1) + "…"; err != nil || a.Reason != want {
		t.Errorf("Reason = %q, %v; want %q", a.Reason, err, want)
	}
}

// "deferred" is valid only with "accepted": 0: a sink that defers takes
// nothing. Anything else is a protocol error, as is a reason that is not a
// string (ADR-0021).
func TestSinkPushDeferredInvalid(t *testing.T) {
	for _, answer := range []string{
		`{"protocol":1,"accepted":2,"deferred":true}`,
		`{"protocol":1,"deferred":true}`,
		`{"protocol":1,"accepted":0,"deferred":"yes"}`,
		`{"protocol":1,"accepted":0,"deferred":true,"reason":5}`,
		// "deferred" is exact-case and given once.
		`{"protocol":1,"accepted":0,"Deferred":true}`,
		`{"protocol":1,"accepted":0,"deferred":false,"deferred":true}`,
	} {
		s, _ := sink(t, answer)
		if _, err := s.Push(context.Background(), "p1", records(3)); !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: err = %v, want a protocol error", answer, err)
		}
	}
}

func TestSinkPushErrorAnswer(t *testing.T) {
	s, _ := sink(t, `{"protocol":1,"error":"disk full"}`)
	_, err := s.Push(context.Background(), "p1", records(1))
	if !errors.Is(err, ErrAnswer) || !strings.Contains(err.Error(), "stele-sink-a push") {
		t.Errorf("err = %v", err)
	}
}

func TestSinkDescribeWrongKind(t *testing.T) {
	r := &fakeRunner{answers: map[string]string{
		"describe": `{"protocol":1,"kind":"source","protocols":[1],"version":"1.0.0"}`,
		"push":     `{"protocol":1,"accepted":1}`,
	}}
	sinks, err := Sinks(steleJSON(t, `{"sinks": [{"name": "a"}]}`), r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sinks[0].Push(context.Background(), "p1", records(1)); !errors.Is(err, ErrProtocol) {
		t.Errorf("err = %v", err)
	}
}
