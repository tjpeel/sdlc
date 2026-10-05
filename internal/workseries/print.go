package workseries

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// PrintPlan writes the deterministic execution order and declared resource map.
func PrintPlan(w io.Writer, p Plan) {
	if w == nil {
		return
	}
	for _, ticket := range p.Tickets {
		fmt.Fprintf(w, "%s priority=%d depends=%s touches=%s\n", filepath.Base(ticket.File), ticket.Priority, strings.Join(ticket.DependsOn, ","), strings.Join(ticket.Touches, ","))
	}
}
