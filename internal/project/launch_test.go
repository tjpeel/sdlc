package project

import (
	"context"
	"reflect"
	"testing"
)

func TestLaunchExactSelectionNoWrites(t *testing.T) {
	root := workRepo(t)
	workTicket(t, root, "JOB:1", "2-second.md")
	workTicket(t, root, "JOB:1", "10-last.md")
	write(t, root, ConfigPath, `{"version":1,"checks":[["go","test","./..."]],"input_files":["README.md"]}`)
	before := workSnapshot(t, root)
	r, e := Launch(context.Background(), root, "JOB:1", "10-last.md", nil)
	if e != nil {
		t.Fatal(e)
	}
	if r.Ticket != ".sdlc/work/JOB:1/tickets/10-last.md" || len(r.Inputs) != 1 || r.Head == "" {
		t.Fatalf("unexpected launch: %+v", r)
	}
	if !reflect.DeepEqual(before, workSnapshot(t, root)) {
		t.Fatal("launch changed source")
	}
	for _, ticket := range []string{"2", "1", "10-LAST.md", "../10-last.md"} {
		if _, e = Launch(context.Background(), root, "JOB:1", ticket, nil); e == nil {
			t.Fatalf("accepted %q", ticket)
		}
	}
}
func TestLaunchMissingConfiguredAndSelectedInput(t *testing.T) {
	root := workRepo(t)
	workTicket(t, root, "JOB", "1-first.md")
	write(t, root, ConfigPath, `{"version":1,"checks":[],"input_files":["missing.md"]}`)
	if _, e := Launch(context.Background(), root, "JOB", "1-first.md", nil); e == nil {
		t.Fatal("missing configured input accepted")
	}
	write(t, root, ConfigPath, `{"version":1,"checks":[],"input_files":[]}`)
	for _, path := range []string{"missing.md", "../README.md"} {
		if _, e := Launch(context.Background(), root, "JOB", "1-first.md", []string{path}); e == nil {
			t.Fatal("bad input accepted")
		}
	}
}
