package webhooks

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"
)

func post(t *testing.T, url, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp
}

func TestCaptureJSON(t *testing.T) {
	l := NewListener(0)
	url, err := l.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	resp := post(t, url, `{"event":"media_buy.activated","id":"mb-1"}`, map[string]string{
		"Authorization": "Bearer super-secret",
		"X-Seller":      "acme",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}

	got := l.Deliveries()
	if len(got) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(got))
	}
	d := got[0]
	if d.Method != http.MethodPost {
		t.Errorf("method %q", d.Method)
	}
	if d.Path != "/hook" {
		t.Errorf("path %q", d.Path)
	}
	if !bytes.Contains(d.Body, []byte("\n  ")) {
		t.Errorf("body should be pretty-printed JSON, got %s", d.Body)
	}
	if !strings.Contains(string(d.Body), "media_buy.activated") {
		t.Errorf("body lost the event: %s", d.Body)
	}
	if d.Headers["Authorization"] != "[redacted]" {
		t.Errorf("authorization header not redacted: %q", d.Headers["Authorization"])
	}
	if d.Headers["X-Seller"] != "acme" {
		t.Errorf("plain header mangled: %q", d.Headers["X-Seller"])
	}
	if d.Received.IsZero() || time.Since(d.Received) > time.Minute {
		t.Errorf("bad received timestamp %v", d.Received)
	}
}

func TestCaptureNonJSON(t *testing.T) {
	l := NewListener(0)
	url, err := l.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	post(t, url, `not json at all`, nil)
	got := l.Deliveries()
	if len(got) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(got))
	}
	if len(got[0].Body) != 0 {
		t.Errorf("non-JSON body should not land in Body: %s", got[0].Body)
	}
	if got[0].RawBody != "not json at all" {
		t.Errorf("raw body %q", got[0].RawBody)
	}
}

func TestTruncation(t *testing.T) {
	l := NewListener(16)
	url, err := l.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	post(t, url, `{"big":"`+strings.Repeat("x", 100)+`"}`, nil)
	got := l.Deliveries()
	if len(got) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(got))
	}
	if !got[0].Truncated {
		t.Error("expected the truncated flag")
	}
}

func TestClearAndOrder(t *testing.T) {
	l := NewListener(0)
	url, err := l.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	post(t, url, `{"n":1}`, nil)
	post(t, url, `{"n":2}`, nil)
	got := l.Deliveries()
	if len(got) != 2 || got[0].ID >= got[1].ID {
		t.Fatalf("deliveries out of order: %+v", got)
	}
	l.Clear()
	if len(l.Deliveries()) != 0 {
		t.Error("clear did not drop the capture")
	}
}

func TestBindsLocalhostOnly(t *testing.T) {
	l := NewListener(0)
	url, err := l.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Errorf("listener must bind loopback only, got %s", url)
	}
}
