package notify

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestOptionsRequireExplicitSupportedModesAndDesktopSound(t *testing.T) {
	for _, options := range []Options{{}, {Mode: "off"}, {Mode: "bell"}, {Mode: "desktop"}, {Mode: "desktop", Sound: true}} {
		if err := validate(options, "darwin"); err != nil {
			t.Fatal(options, err)
		}
	}
	for _, options := range []Options{{Mode: "unknown"}, {Sound: true}, {Mode: "off", Sound: true}, {Mode: "bell", Sound: true}} {
		if err := options.Validate(); err == nil {
			t.Fatal("invalid notification options accepted", options)
		}
	}
	if err := validate(Options{Mode: "desktop"}, "linux"); err == nil || !strings.Contains(err.Error(), "macOS") {
		t.Fatal("unsupported desktop platform accepted", err)
	}
	if runtime.GOOS != "darwin" {
		if _, err := New(Options{Mode: "desktop"}, io.Discard); err == nil {
			t.Fatal("unsupported native desktop sender created")
		}
	}
}

func TestDefaultOffAndExplicitBell(t *testing.T) {
	var output bytes.Buffer
	for _, mode := range []string{"", "off"} {
		sender, err := New(Options{Mode: mode}, &output)
		if err != nil || sender.Send(context.Background(), "SDLC: run blocked.") != nil || output.Len() != 0 {
			t.Fatal("default notification emitted output", err)
		}
	}
	if _, err := New(Options{Mode: "bell"}, nil); err == nil {
		t.Fatal("bell accepted missing output")
	}
	sender, err := New(Options{Mode: "bell"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.Send(context.Background(), "message is not written"); err != nil || !bytes.Equal(output.Bytes(), []byte{7}) {
		t.Fatal("bell did not emit exactly BEL", output.Bytes(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sender.Send(ctx, "ignored"); err == nil || output.Len() != 1 {
		t.Fatal("cancelled bell emitted output")
	}
}

type failingOutput struct{}

func (failingOutput) Write([]byte) (int, error) { return 0, errors.New("private diagnostic") }

func TestBellFailureIsRedacted(t *testing.T) {
	sender, err := New(Options{Mode: "bell"}, failingOutput{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.Send(context.Background(), "ignored"); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("writer failure ignored or leaked", err)
	}
}

func TestDesktopUsesFixedScriptArgumentsEnvironmentAndDeadlineWithoutExecuting(t *testing.T) {
	for _, sound := range []bool{false, true} {
		calls := 0
		message := `SDLC: run blocked (run abcdef01). " & do shell script "ignored"`
		sender := desktopSender{sound: sound, home: "/disposable-home", execute: func(ctx context.Context, path string, args, environment []string) error {
			calls++
			wantSound := "silent"
			if sound {
				wantSound = "sound"
			}
			if path != "/usr/bin/osascript" || !reflect.DeepEqual(args, []string{"-e", desktopScript, message, wantSound}) {
				t.Fatal("desktop interpolated script or changed executable", path, args)
			}
			if strings.Contains(desktopScript, message) || strings.Contains(desktopScript, "do shell script") || !strings.Contains(desktopScript, `sound name "Glass"`) {
				t.Fatal("unsafe script construction")
			}
			if !reflect.DeepEqual(environment, []string{"PATH=/usr/bin:/bin", "HOME=/disposable-home"}) {
				t.Fatal("helper inherited extra environment", environment)
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 2*time.Second {
				t.Fatal("desktop helper is not bounded")
			}
			return errors.New("private helper diagnostic")
		}}
		if err := sender.Send(context.Background(), message); err == nil || strings.Contains(err.Error(), "private") || calls != 1 {
			t.Fatal("desktop failure ignored, leaked or retried", err, calls)
		}
	}
}

func TestDesktopRespectsEarlierContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	original, _ := ctx.Deadline()
	sender := desktopSender{execute: func(child context.Context, _ string, _, _ []string) error {
		deadline, ok := child.Deadline()
		if !ok || deadline != original {
			t.Fatal("desktop replaced an earlier caller deadline")
		}
		return nil
	}}
	if err := sender.Send(ctx, "fixed message"); err != nil {
		t.Fatal(err)
	}
}
