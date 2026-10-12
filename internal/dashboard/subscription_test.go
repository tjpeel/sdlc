package dashboard

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runusage"
)

func TestRecordedRemainingPercentagesAreReadable(t *testing.T) {
	var usage runusage.SubscriptionUsage
	if err := json.Unmarshal([]byte(`{"windows":{"codex/primary":{"before_remaining_percent":80,"after_remaining_percent":75,"used_percentage_points":5,"status":"observed"},"codex/secondary":{"before_remaining_percent":60,"after_remaining_percent":90,"status":"window_changed"}}}`), &usage); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	WriteSubscriptions(&output, map[string]*runusage.SubscriptionUsage{"codex": &usage, "claude": {}})
	for _, expected := range []string{"80.0% before -> 75.0% after; used 5.0 percentage points", "60.0% before -> 90.0% after; used unknown (window_changed)", "claude: before not captured; after not captured; used unknown"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %q in %s", expected, output.String())
		}
	}
}
