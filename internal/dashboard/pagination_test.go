package dashboard

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runstatus"
)

func TestPagesBoundHistoryAndKeepGlobalPriority(t *testing.T) {
	now := time.Now().UTC()
	var views []runstatus.View
	for i := 0; i < 23; i++ {
		views = append(views, runstatus.View{ID: fmt.Sprintf("%024x", i+1), State: "ready", Available: true, Stopped: true, NeedsAttention: true, UpdatedAt: now.Add(time.Duration(i) * time.Minute)})
	}
	views = append(views, runstatus.View{ID: strings.Repeat("a", 24), State: "implementing", Available: true, Live: true, UpdatedAt: now.Add(-time.Hour)}, runstatus.View{ID: strings.Repeat("b", 24), State: "awaiting_reviewer", Available: true, Stopped: true, NeedsAttention: true, UpdatedAt: now.Add(-2 * time.Hour)})
	seen := map[string]bool{}
	for n, expected := range []int{10, 10, 5} {
		rows, p := Page(views, n+1)
		if len(rows) != expected || p.Total != 25 || p.Pages != 3 || p.PageSize != 10 || p.Page != n+1 {
			t.Fatalf("page %d: %+v / %d", n+1, p, len(rows))
		}
		for _, v := range rows {
			if seen[v.ID] {
				t.Fatal("repeated run between pages")
			}
			seen[v.ID] = true
		}
		if n == 0 && (rows[0].State != "awaiting_reviewer" || rows[1].State != "implementing") {
			t.Fatal("completed history displaced current work")
		}
	}
	if len(seen) != len(views) {
		t.Fatal("history lost runs")
	}
	rows, p := Page(views, 999)
	if p.Page != 3 || len(rows) != 5 {
		t.Fatal("removed last page was not clamped")
	}
	rows, p = Page(nil, 999)
	if p.Page != 1 || p.Pages != 1 || p.Total != 0 || len(rows) != 0 {
		t.Fatal("empty registry paging failed")
	}
	var out bytes.Buffer
	if err := ListPage(&out, views, now, 2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "25 runs") || !strings.Contains(out.String(), "Page 2/3") || !strings.Contains(out.String(), "showing 11–20 of 25") {
		t.Fatal(out.String())
	}
}
