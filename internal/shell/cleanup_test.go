package shell

import (
	"context"
	"fmt"
	"os/exec"
	"reflect"
	"testing"
)

func TestNativeDashboardRemovalUsesSelectedHistoryScope(t *testing.T) {
	for _, test := range []struct {
		scope, action, suffix, expected string
	}{
		{"project", "remove", "", "project"},
		{"all", "remove", "", "installation"},
		{"all", "forget", "", "installation"},
		{"project", "remove", " --scope all", "installation"},
		{"project", "forget", " --scope=all", "installation"},
	} {
		t.Run(test.scope+"/"+test.action+test.suffix, func(t *testing.T) {
			var actual []string
			m := NewModel(context.Background(), Config{
				Commands: []Command{{Name: "dashboard"}, {Name: "dashboard remove", Native: true}, {Name: "dashboard forget", Native: true}},
				Read: func(context.Context, string, []string) (string, error) {
					return "offline dashboard", nil
				},
				Execute: func(_ context.Context, _ string, args []string) (*exec.Cmd, error) {
					actual = append([]string(nil), args...)
					return nil, fmt.Errorf("offline native boundary")
				},
			})
			if cmd := enter(m, "/scope "+test.scope); cmd != nil {
				m.Update(cmd())
			}
			enter(m, "/dashboard "+test.action+" --all --yes"+test.suffix)
			expected := []string{"dashboard", test.action, "--all", "--yes", "--scope", test.expected}
			if test.suffix == " --scope=all" {
				expected = []string{"dashboard", test.action, "--all", "--yes", "--scope=" + test.expected}
			}
			if !reflect.DeepEqual(actual, expected) || m.monitor {
				t.Fatalf("native args %v; want %v, monitor=%t", actual, expected, m.monitor)
			}
		})
	}
}
