package workrun

import (
	"context"
	"errors"
	"fmt"

	"github.com/tjpeel/sdlc/internal/githubauth"
)

// Run the current controller's reader/replier with the pinned runtime's gh,
// without depending on capabilities in historical publisher binaries.
func (p DockerPublisher) withFeedbackPublisher(ctx context.Context, plan Plan, operation func(GitHubPublisher) error) (resultErr error) {
	frozen := plan.PublicationIdentity
	if frozen == nil {
		return fmt.Errorf("PR feedback requires frozen publication identity")
	}
	if err := frozen.Validate(); err != nil {
		return err
	}
	if p.ValidatePair != nil {
		if err := p.ValidatePair(); err != nil {
			return err
		}
	}
	if p.Runtime.Docker == nil || p.ImageID == "" {
		return fmt.Errorf("feedback requires pinned runtime")
	}
	state, err := p.Runtime.Status(ctx)
	if err != nil || state.ImageID != p.ImageID {
		return fmt.Errorf("feedback runtime differs from pinned image")
	}
	session, err := p.Auth.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("GitHub stored login unavailable")
	}
	defer func() {
		if err := session.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("feedback credential lease cleanup failed"))
		}
	}()
	if p.ValidatePair != nil {
		if err := p.ValidatePair(); err != nil {
			return err
		}
	}
	profile, profileErr := githubauth.NormalizeProfile(plan.GitHubProfile)
	frozenProfile, frozenErr := githubauth.NormalizeProfile(frozen.GitHubProfile)
	identity, err := session.Identity(ctx)
	if err != nil || profileErr != nil || frozenErr != nil || profile != frozenProfile || session.Profile != frozenProfile || session.ImageID != p.ImageID || session.Volume != frozen.GitHubVolume || identity.ID != frozen.GitHubID || identity.Login != frozen.GitHubLogin {
		return fmt.Errorf("feedback GitHub session differs from frozen publication identity")
	}
	collector := GitHubPublisher{Frozen: frozen, Command: func(ctx context.Context, name string, argv ...string) ([]byte, error) {
		if name != "gh" || len(argv) < 1 || argv[0] != "api" && argv[0] != "pr" {
			return nil, fmt.Errorf("unsupported feedback controller command")
		}
		return p.ciControllerCommand(ctx, session.Volume, argv)
	}}
	return operation(collector)
}
func (p DockerPublisher) Feedback(ctx context.Context, plan Plan, pr Publication) (comments []PRFeedbackComment, err error) {
	err = p.withFeedbackPublisher(ctx, plan, func(collector GitHubPublisher) error {
		var callErr error
		comments, callErr = collector.Feedback(ctx, plan, pr)
		return callErr
	})
	if err != nil {
		comments = nil
	}
	return
}
func (p DockerPublisher) ReplyFeedback(ctx context.Context, plan Plan, pr Publication, comment PRFeedbackComment, marker, body string) (reply PRFeedbackReply, err error) {
	err = p.withFeedbackPublisher(ctx, plan, func(collector GitHubPublisher) error {
		var callErr error
		reply, callErr = collector.ReplyFeedback(ctx, plan, pr, comment, marker, body)
		return callErr
	})
	if err != nil {
		reply = PRFeedbackReply{}
	}
	return
}

// ValidateFeedbackBoundary checks the frozen PR without reading or posting comments.
func (p DockerPublisher) ValidateFeedbackBoundary(ctx context.Context, plan Plan, pr Publication) error {
	return p.withFeedbackPublisher(ctx, plan, func(collector GitHubPublisher) error {
		return collector.feedbackBoundary(ctx, plan, pr)
	})
}

func (p DockerPublisher) ReplyFeedbackConversation(ctx context.Context, plan Plan, pr Publication, target PRFeedbackComment, comments []PRFeedbackComment, marker, body string) (reply PRFeedbackReply, err error) {
	err = p.withFeedbackPublisher(ctx, plan, func(collector GitHubPublisher) error {
		var callErr error
		reply, callErr = collector.ReplyFeedbackConversation(ctx, plan, pr, target, comments, marker, body)
		return callErr
	})
	if err != nil {
		reply = PRFeedbackReply{}
	}
	return
}
