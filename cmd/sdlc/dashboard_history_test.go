package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestDashboardJSONPagesMoreThanTenRuns(t *testing.T) {
	root, marker, journals := dashboardFixture(t)
	registry := runstatus.New(filepath.Join(root, "installation"))
	for i := 1; i <= 21; i++ {
		j := journals[0]
		j.ID, j.State = fmt.Sprintf("%024x", i), "ready"
		dir := filepath.Join(root, "history", j.ID)
		j.Workspace = filepath.Join(dir, "workspace")
		if err := os.MkdirAll(j.Workspace, 0700); err != nil {
			t.Fatal(err)
		}
		if err := workrun.Save(dir, &j); err != nil {
			t.Fatal(err)
		}
		if err := registry.Register(dir, j); err != nil {
			t.Fatal(err)
		}
		a := runstatus.Snapshot{Version: 1, ID: j.ID, ControllerID: strings.Repeat("d", 24), HeartbeatAt: time.Now().UTC(), Stopped: true}
		data, _ := json.Marshal(a)
		if err := os.WriteFile(filepath.Join(dir, "activity.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := dashboardTree(t, root)
	seen := map[string]bool{}
	for page, count := range []int{10, 10, 3} {
		var out bytes.Buffer
		if err := dashboardCommand(context.Background(), []string{"--json", "--page", fmt.Sprint(page + 1)}, &out); err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Page     int `json:"page"`
			PageSize int `json:"page_size"`
			Total    int `json:"total"`
			Pages    int `json:"pages"`
			Runs     []struct {
				ID    string
				State string
			} `json:"runs"`
		}
		if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Page != page+1 || doc.PageSize != 10 || doc.Total != 23 || doc.Pages != 3 || len(doc.Runs) != count {
			t.Fatal(out.String())
		}
		for _, row := range doc.Runs {
			if seen[row.ID] {
				t.Fatal("run repeated")
			}
			seen[row.ID] = true
		}
		if page == 0 && (doc.Runs[0].State != "waiting_for_human" || doc.Runs[1].State != "implementing") {
			t.Fatal("current work did not precede completed history")
		}
	}
	assertDashboardReadOnly(t, root, marker, before)
}

func TestDashboardForgetRetainsCheckpointAndRestoresOnRegister(t *testing.T) {
	root, marker, journals := dashboardFixture(t)
	j := journals[1]
	dir := filepath.Dir(j.Workspace)
	if err := os.WriteFile(filepath.Join(dir, "run.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	before := dashboardTree(t, dir)
	var out bytes.Buffer
	registryBefore := dashboardTree(t, filepath.Join(root, "installation"))
	if err := dashboardCommand(context.Background(), []string{"remove", "--run", j.ID[:3]}, &out); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous remove: %v", err)
	}
	assertDashboardReadOnly(t, filepath.Join(root, "installation"), marker, registryBefore)
	if err := dashboardCommand(context.Background(), []string{"forget", "--run", j.ID}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Saved work remains") {
		t.Fatal(out.String())
	}
	assertDashboardReadOnly(t, dir, marker, before)
	registry := runstatus.New(filepath.Join(root, "installation"))
	views, err := registry.List(time.Now().UTC())
	if err != nil || len(views) != 1 {
		t.Fatalf("registration remains: %d %v", len(views), err)
	}
	if err := dashboardCommand(context.Background(), []string{"forget", "--run", journals[0].ID}, &out); err == nil {
		t.Fatal("live run forgotten")
	}
	if err := registry.Register(dir, j); err != nil {
		t.Fatal(err)
	}
	views, err = registry.List(time.Now().UTC())
	if err != nil || len(views) != 2 {
		t.Fatalf("retained run failed to re-register: %d %v", len(views), err)
	}
}

func TestDashboardExportIsPrivateAndLeavesRunUnchanged(t *testing.T) {
	root, marker, journals := dashboardFixture(t)
	destination, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(destination, 0700); err != nil {
		t.Fatal(err)
	}
	before := dashboardTree(t, root)
	var out bytes.Buffer
	if err := dashboardCommand(context.Background(), []string{"export", "--run", journals[1].ID, "--to", destination}, &out); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(destination, journals[1].ID+".report.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("export permissions: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), journals[1].Publication.URL) || !strings.Contains(string(data), journals[1].Outcome.Questions[0]) {
		t.Fatal("report omitted evidence or pending question")
	}
	if strings.Contains(string(data), journals[1].Instructions) || strings.Contains(string(data), "private-recent-event") {
		t.Fatal("export copied instructions or raw output")
	}
	if err := dashboardCommand(context.Background(), []string{"export", "--run", journals[1].ID, "--to", destination}, &out); err == nil {
		t.Fatal("existing report overwritten")
	}
	assertDashboardReadOnly(t, root, marker, before)
}

func TestDashboardPagingInputAndValidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var commands []string
	for cmd := range readDashboardInput(ctx, strings.NewReader("ignore\nn\np\n q \n")) {
		commands = append(commands, cmd)
	}
	if strings.Join(commands, ",") != "n,p,q" {
		t.Fatal(commands)
	}
	page := 1
	for _, cmd := range commands[:2] {
		page, _ = dashboardPageAction(page, cmd)
	}
	if page != 1 {
		t.Fatal(page)
	}
	if page, quit := dashboardPageAction(1, "q"); page != 1 || !quit {
		t.Fatal("quit did not stop view")
	}
	if page, _ := dashboardPageAction(1, "p"); page != 1 {
		t.Fatal("previous went before first page")
	}
	for _, args := range [][]string{{"--page", "0"}, {"--page", "-1"}, {"--run", "123456", "--page", "2"}, {"--json", "--notify", "bell"}, {"--once", "--notify", "bell"}, {"--sound"}, {"--notify", "invalid"}} {
		if _, err := parseDashboardOptions(args); err == nil {
			t.Fatalf("invalid options accepted: %v", args)
		}
	}
}
