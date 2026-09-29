package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/conformance"
	"github.com/sujanchalla0510/adcp-test/internal/mockcfg"
	"github.com/sujanchalla0510/adcp-test/internal/mockserver"
)

// lifecycleMock is a mock seller with the media-buy state machine wired
// to template responses, mirroring examples/mock-seller.yaml.
const lifecycleMock = `
mocks:
  - name: seller
    listen: :0
    routes:
      - match: { tool: create_media_buy }
        respond:
          template: create.json
      - match: { tool: update_media_buy }
        state_machine: media-buy-lifecycle
        respond:
          template: update.json
`

func writeTemplates(t *testing.T, dir string) {
	t.Helper()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("create.json", `{"media_buy_id": "mb-{{call}}", "status": "draft"}`)
	write("update.json", `{"media_buy_id": "{{entity_id}}", "status": "{{state}}"}`)
}

func TestRunAgainstMock(t *testing.T) {
	dir := t.TempDir()
	writeTemplates(t, dir)
	cfg, err := mockcfg.Parse([]byte(lifecycleMock))
	if err != nil {
		t.Fatal(err)
	}
	svc := cfg.Services()[0]
	svc.Listen = ":0"
	srv, err := mockserver.New(svc, mockserver.Options{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	url, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rep, err := Run(ctx, url, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Checks) != 6 {
		t.Fatalf("expected 6 checks, got %d: %+v", len(rep.Checks), rep.Checks)
	}
	want := []string{
		"lifecycle:create",
		"lifecycle:activate",
		"lifecycle:pause",
		"lifecycle:resume",
		"lifecycle:cancel",
		"lifecycle:illegal-transition-rejected",
	}
	for i, name := range want {
		if rep.Checks[i].Name != name {
			t.Errorf("check %d: want %q, got %q", i, name, rep.Checks[i].Name)
		}
		if rep.Checks[i].Status != conformance.StatusPass {
			t.Errorf("check %q: want pass, got %s (%s)",
				name, rep.Checks[i].Status, rep.Checks[i].Detail)
		}
	}
	if !rep.AllPassed() {
		t.Error("AllPassed should be true")
	}
	if rep.MediaBuyID == "" {
		t.Error("MediaBuyID should be set")
	}
	// The illegal transition must have been rejected with -32001.
	last := rep.Checks[5]
	if !strings.Contains(last.Detail, "-32001") {
		t.Errorf("illegal transition detail should cite -32001, got: %s", last.Detail)
	}
}

func TestRunEmptyTarget(t *testing.T) {
	if _, err := Run(context.Background(), "", Options{}); err == nil {
		t.Error("expected an error for an empty target")
	}
}
