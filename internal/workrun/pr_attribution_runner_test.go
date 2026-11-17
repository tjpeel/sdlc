package workrun

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type attributionPublisher struct {
	*fakePublisher
	bodies []string
}

func (p *attributionPublisher) Publish(ctx context.Context, plan Plan, workspace, directory string, previous Publication, out io.Writer) (Publication, error) {
	p.bodies = append(p.bodies, plan.PRBody)
	return p.fakePublisher.Publish(ctx, plan, workspace, directory, previous, out)
}

func TestRunnerCleansGeneratedAndPreviouslySavedPRFooter(t *testing.T) {
	footer := "🤖 Generated with [Claude Code](https://claude.com/claude-code)"
	dir, j := testRun(t)
	generated := testOutcome("implemented")
	generated.PRBody = "Useful description\n\n" + footer + "\n"
	provider := &fakeProvider{outcomes: []Outcome{generated, testOutcome("reviewed")}}
	base := &fakePublisher{err: errors.New("temporary publication failure")}
	pub := &attributionPublisher{fakePublisher: base}
	r := fakeRunner(provider, &fakeChecker{}, base, &fakeRepository{})
	r.Publisher = pub
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if len(pub.bodies) != 1 || strings.Contains(pub.bodies[0], footer) || strings.Contains(j.Plan.PRBody, footer) {
		t.Fatal("generated footer reached publication")
	}
	// A journal captured by an older controller can still contain the footer.
	j.Plan.PRBody = "Useful description\n\n" + footer + "\n"
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	base.err = nil
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if len(pub.bodies) != 2 || strings.Contains(pub.bodies[1], footer) {
		t.Fatal("saved footer reached publication")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(saved.Plan.PRBody, footer) {
		t.Fatal("saved PR metadata retains footer")
	}
}

func TestFeedbackResumeCleansSavedFooterEvenWithoutPublication(t *testing.T) {
	dir, j, r, pub, _ := feedbackReadyRun(t)
	footer := "🤖 Generated with [Claude Code](https://claude.com/claude-code)"
	j.Plan.PRBody = "Useful description\n\n" + footer + "\n"
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	provider := &localAssessmentProvider{j: j}
	r.Publisher = &localFeedbackPublisher{feedbackPublisherFake: pub}
	r.Provider = provider
	r.ProcessPRFeedback = true
	r.LocalPRFeedback = "The current behaviour is already correct."
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(saved.Plan.PRBody, footer) {
		t.Fatal("saved footer was not reconciled")
	}
}
