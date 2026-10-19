// Package workrun coordinates one explicitly selected ticket. Provider clients
// do engineering work; the controller owns execution, evidence and publication.
package workrun

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/tjpeel/sdlc/internal/headroom"
	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/runtimepins"
)

type Model struct {
	Provider string `json:"provider"`
	Name     string `json:"model"`
	Effort   string `json:"effort"`
}

type Roles struct {
	AutoAcceptRecommendations bool   `json:"auto_accept_recommendations,omitempty"`
	ReviewEffortPolicy        string `json:"review_effort_policy,omitempty"`
	ImplementationSpeed       string `json:"implementation_speed,omitempty"`
	ReviewSpeed               string `json:"review_speed,omitempty"`
	Implementation            Model  `json:"implementation"`
	Review                    Model  `json:"review"`
}

type Input struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type PublicationIdentity struct {
	GitHubProfile  string `json:"github_profile"`
	RepositoryID   int64  `json:"repository_id"`
	RepositoryName string `json:"repository_name"`
	GitHubVolume   string `json:"github_volume"`
	ProfileID      string `json:"profile_id"`
	GitHubID       int64  `json:"github_id"`
	GitHubLogin    string `json:"github_login"`
	GitName        string `json:"git_name"`
	GitEmail       string `json:"git_email"`
	SSHPublicKey   string `json:"ssh_public_key"`
	SSHFingerprint string `json:"ssh_fingerprint"`
}

type Plan struct {
	NonDefaultCIWorkflow string                       `json:"non_default_ci_workflow,omitempty"`
	Adoption             *PRAdoption                  `json:"adoption,omitempty"`
	DependencyContext    *DependencyContext           `json:"dependency_context,omitempty"`
	Derived              *DerivedBoundary             `json:"derived,omitempty"`
	RuntimeProvenance    *RuntimeProvenance           `json:"runtime_provenance,omitempty"`
	NugetCache           *project.NugetCacheBudget    `json:"nuget_cache,omitempty"`
	PRMetadataPolicy     *PRMetadataPolicy            `json:"pr_metadata_policy,omitempty"`
	RuntimeName          string                       `json:"runtime_name,omitempty"`
	Headroom             headroom.Config              `json:"headroom,omitzero"`
	GitHubProfile        string                       `json:"github_profile,omitempty"`
	SigningProfile       string                       `json:"signing_profile,omitempty"`
	PublicationIdentity  *PublicationIdentity         `json:"publication_identity,omitempty"`
	Root                 string                       `json:"root"`
	Reference            string                       `json:"reference"`
	Ticket               string                       `json:"ticket"`
	StartingSHA          string                       `json:"starting_sha"`
	SourceSHA            string                       `json:"source_sha"`
	Branch               string                       `json:"branch"`
	Base                 string                       `json:"base"`
	BaseSHA              string                       `json:"base_sha"`
	Repository           string                       `json:"repository"`
	Inputs               []Input                      `json:"inputs"`
	InputContext         []project.RequirementContext `json:"input_context,omitempty"`
	CheckInputs          []Input                      `json:"check_inputs"`
	CheckSelection       string                       `json:"check_selection,omitempty"`
	Checks               [][]string                   `json:"checks"`
	DockerTests          bool                         `json:"docker_tests"`
	SigningImage         string                       `json:"signing_image,omitempty"`
	DaemonImage          string                       `json:"daemon_image,omitempty"`
	DaemonMode           string                       `json:"daemon_mode,omitempty"`
	Roles                Roles                        `json:"roles"`
	PRTitle              string                       `json:"pr_title,omitempty"`
	PRBody               string                       `json:"pr_body,omitempty"`
	Restack              *RestackBoundary             `json:"restack,omitempty"`
}

// DerivedBoundary authorises a branch-only aggregate of exact parent snapshots.
type DerivedBoundary struct {
	Parents []DerivedParent `json:"parents"`
}
type DerivedParent struct {
	Ticket, RunID, Branch, Base, BaseSHA, HeadSHA, State, MergeSHA string
	Number                                                         int
}

// RestackBoundary is the exact remote PR boundary which a restack may move.
// It prevents a resumed controller from retargeting an unrelated PR.
type RestackBoundary struct {
	Base    string `json:"base"`
	BaseSHA string `json:"base_sha"`
	HeadSHA string `json:"head_sha"`
	Number  int    `json:"number"`
}

type BranchSnapshot struct {
	DependencyContext *DependencyContext `json:"dependency_context,omitempty"`
	Branch            string             `json:"branch"`
	SHA               string             `json:"sha"`
	Bundle            string             `json:"bundle"`
}

type DependencyContext struct {
	Identity, IntegrationBranch, IntegrationSHA, SourceSHA, SignedSHA, Tree string
	Owner, Ticket                                                           string
	Parents                                                                 []DerivedParent
	Evidence                                                                CheckEvidence
}

type RemotePR struct {
	State    string `json:"state"`
	Base     string `json:"base"`
	BaseSHA  string `json:"base_sha"`
	HeadSHA  string `json:"head_sha"`
	MergeSHA string `json:"merge_sha"`
}

type RebaseResult struct {
	Conflict bool     `json:"conflict"`
	Paths    []string `json:"paths"`
}

// Reconciliation is checkpointed before the workspace is changed. Its source
// and target revisions form the idempotence key used by DockerRepository.
type Reconciliation struct {
	OldSource string         `json:"old_source"`
	Snapshot  BranchSnapshot `json:"snapshot"`
	State     string         `json:"state"`
	Result    RebaseResult   `json:"result"`
}

// ValidateSidecarImages accepts absent pins for journals written before runtime
// sidecars were selected per installation. Callers retain the historical defaults.
func (plan Plan) ValidateSidecarImages() error {
	if plan.CheckSelection != "" && plan.CheckSelection != "explicit" && plan.CheckSelection != "runtime" {
		return fmt.Errorf("unsupported check selection")
	}
	if _, err := project.ResolveNugetCacheBudget(plan.NugetCache); err != nil {
		return err
	}
	if plan.DaemonMode != "" && plan.DaemonMode != SharedTestDaemon {
		return fmt.Errorf("unsupported repository check daemon mode")
	}
	if plan.DaemonMode == SharedTestDaemon && (!plan.DockerTests || plan.DaemonImage == "") {
		return fmt.Errorf("shared Docker checks require an exact daemon pin")
	}
	if err := plan.Headroom.Validate(); err != nil {
		return err
	}
	if plan.SigningImage != "" {
		if err := runtimepins.ValidateSigningImage(plan.SigningImage); err != nil {
			return err
		}
	}
	if plan.DaemonImage != "" {
		if err := runtimepins.ValidateDaemonImage(plan.DaemonImage); err != nil {
			return err
		}
	}
	return nil
}

type Finding struct {
	Priority       string `json:"priority"`
	Path           string `json:"path"`
	Line           int    `json:"line"`
	Scenario       string `json:"scenario"`
	Recommendation string `json:"recommendation"`
}

// ReviewCoverage records inspection scope separately from test evidence.
type ReviewFileInspection struct {
	Path     string `json:"path"`
	Complete bool   `json:"complete"`
	Evidence string `json:"evidence"`
}

type ReviewCoverage struct {
	Inspection    []ReviewFileInspection `json:"inspection,omitempty"`
	BaseSHA       string                 `json:"base_sha"`
	HeadSHA       string                 `json:"head_sha"`
	Scope         string                 `json:"scope"`
	Gaps          []string               `json:"gaps"`
	CompletedGaps []string               `json:"completed_gaps,omitempty"`
}

// ReviewReport retains bounded review evidence across repair rounds.
type ReviewReport struct {
	Findings        []Finding       `json:"findings,omitempty"`
	Publication     Publication     `json:"publication"`
	Attempt         int             `json:"attempt"`
	Coverage        *ReviewCoverage `json:"coverage,omitempty"`
	Limitations     []string        `json:"limitations,omitempty"`
	Recommendations []Finding       `json:"recommendations,omitempty"`
}

type Outcome struct {
	Recommendations []Finding       `json:"recommendations,omitempty"`
	ReviewCoverage  *ReviewCoverage `json:"review_coverage,omitempty"`
	// PRFeedbackAssessments may cover any subset of the current requested batch.
	PRFeedbackAssessments []PRFeedbackAssessment       `json:"pr_feedback_assessments,omitempty"`
	RepositoryRequests    []CompanionRepositoryRequest `json:"repository_requests,omitempty"`
	DiagnosticRequests    []VerificationRequest        `json:"diagnostic_requests,omitempty"`
	VerificationRequests  []VerificationRequest        `json:"verification_requests,omitempty"`
	Status                string                       `json:"status"`
	Summary               string                       `json:"summary"`
	Questions             []string                     `json:"questions"`
	Findings              []Finding                    `json:"findings"`
	LocalReview           bool                         `json:"local_review"`
	Limitations           []string                     `json:"limitations"`
	PRTitle               string                       `json:"pr_title"`
	PRBody                string                       `json:"pr_body"`
}

type Session struct {
	Speed        string
	Diagnostics  func(context.Context, LiveDiagnosticRequest) (LiveDiagnosticResponse, error) `json:"-"`
	Model        Model
	Role         string
	Workspace    string
	Directory    string
	Prompt       string
	Schema       string
	ResumeID     string
	Instructions string
	Headroom     headroom.Config
}

type SessionResult struct {
	ReviewInterruption *ReviewInterruption
	Outcome            Outcome
	SessionID          string
	ReportedModel      string
	HeadroomStats      *headroom.Stats
}

type Provider interface {
	Status(context.Context, string) (string, error)
	Execute(context.Context, Session, io.Writer, io.Writer) (SessionResult, error)
}

type CheckEvidence struct {
	Output []CommandOutput `json:"output,omitempty"`
	Key    string          `json:"key,omitempty"`
	// Only completed semantic failure on this frozen boundary may be reused
	// while a new diagnostic runs. Legacy and operational failures have no key.
	CompletedFailureKey      string     `json:"completed_failure_key,omitempty"`
	CompletedFailureFeedback string     `json:"completed_failure_feedback,omitempty"`
	Head                     string     `json:"head,omitempty"`
	Tree                     string     `json:"tree"`
	Passed                   bool       `json:"passed"`
	Commands                 [][]string `json:"commands"`
	Log                      string     `json:"log"`
}

type Checker interface {
	Check(context.Context, string, [][]string, io.Writer) error
}

type Publication struct {
	URL     string `json:"url"`
	Number  int    `json:"number"`
	BaseSHA string `json:"base_sha"`
	HeadSHA string `json:"head_sha"`
}

type CIResult struct {
	WorkflowPreparation *CIWorkflowPreparation `json:"workflow_preparation,omitempty"`
	SonarDuplication    []SonarDuplication     `json:"sonar_duplication,omitempty"`
	FailureContext      *CIFailureContext      `json:"failure_context,omitempty"`
	Diagnostics         []CIAnnotation         `json:"diagnostics,omitempty"`
	Status              string                 `json:"status"`
	Details             string                 `json:"details"`
}

type Publisher interface {
	Publish(context.Context, Plan, string, string, Publication, io.Writer) (Publication, error)
	Checks(context.Context, Plan, Publication) (CIResult, error)
}

type Journal struct {
	RebaseMonitoring          *RebaseMonitoring       `json:"rebase_monitoring,omitempty"`
	RebasedAdoptions          []PRAdoption            `json:"rebased_adoption_boundaries,omitempty"`
	ResumeRebase              bool                    `json:"resume_rebase_pending,omitempty"`
	CIWorkflowDispatches      []CIWorkflowDispatch    `json:"ci_workflow_dispatches,omitempty"`
	AutoRecommendationHeads   []string                `json:"auto_recommendation_heads,omitempty"`
	PRCheckpoints             []PRCheckpoint          `json:"pr_checkpoints,omitempty"`
	ReviewEffort              *ReviewEffortDecision   `json:"review_effort_selection,omitempty"`
	ReviewInterruption        *ReviewInterruption     `json:"interrupted_independent_review,omitempty"`
	ReviewSkip                *ReviewSkip             `json:"independent_review_skipped,omitempty"`
	CIFeedbackRecoveries      []CIFeedbackRecovery    `json:"ci_feedback_recoveries,omitempty"`
	PushRepair                *PushProtectionRepair   `json:"push_protection_repair,omitempty"`
	RestackNeedsChecks        bool                    `json:"restack_needs_checks,omitempty"`
	DeferredAnswer            string                  `json:"deferred_answer,omitempty"`
	DependencyInvalid         bool                    `json:"dependency_invalid,omitempty"`
	RunLimitOverride          *RunLimitOverride       `json:"run_limit_override,omitempty"`
	FormatterPreparation      []VerificationResult    `json:"formatter_preparation,omitempty"`
	PRFeedback                *PRFeedbackHistory      `json:"pr_feedback,omitempty"`
	SessionTurnExtension      *SessionTurnExtension   `json:"session_turn_extension,omitempty"`
	EffectiveChecks           [][]string              `json:"effective_checks,omitempty"`
	ConfiguredFeedbackKey     string                  `json:"configured_feedback_key,omitempty"`
	CompanionRepositories     []CompanionRepository   `json:"companion_repositories,omitempty"`
	PRMetadataCorrections     int                     `json:"pr_metadata_corrections,omitempty"`
	InputRecoveries           []InputRecovery         `json:"input_recoveries,omitempty"`
	PendingInputs             *PendingInputRecovery   `json:"pending_inputs,omitempty"`
	Timings                   Timings                 `json:"timings"`
	Version                   int                     `json:"version"`
	ID                        string                  `json:"id"`
	Plan                      Plan                    `json:"plan"`
	State                     string                  `json:"state"`
	Workspace                 string                  `json:"workspace"`
	SessionID                 string                  `json:"session_id"`
	ReportedModel             string                  `json:"reported_model,omitempty"`
	Publication               Publication             `json:"publication"`
	ImageID                   string                  `json:"image_id"`
	LiveDiagnostics           []VerificationResult    `json:"live_diagnostics,omitempty"`
	Diagnostics               []VerificationResult    `json:"diagnostics,omitempty"`
	DiagnosticFeedbackKey     string                  `json:"diagnostic_feedback_key,omitempty"`
	DiagnosticFeedbackCount   int                     `json:"diagnostic_feedback_count,omitempty"`
	VerificationFeedbackKey   string                  `json:"verification_feedback_key,omitempty"`
	VerificationFeedbackCount int                     `json:"verification_feedback_count,omitempty"`
	Verification              []VerificationResult    `json:"additional_verification,omitempty"`
	Evidence                  CheckEvidence           `json:"evidence"`
	Outcome                   Outcome                 `json:"outcome"`
	ReviewRepairIncreases     []ReviewRepairIncrease  `json:"review_repair_increases,omitempty"`
	ReviewHumanInput          *ReviewHumanInput       `json:"review_human_input,omitempty"`
	ReviewRepairBudget        *ReviewRepairBudget     `json:"review_repair_budget,omitempty"`
	ReviewContinuation        *ReviewContinuation     `json:"review_continuation,omitempty"`
	ReviewReports             []ReviewReport          `json:"review_reports,omitempty"`
	ReviewAssessment          *ReviewAssessment       `json:"review_assessment,omitempty"`
	CIRepairBudget            *CIRepairBudget         `json:"ci_repair_budget,omitempty"`
	ConfiguredRepairBudget    *ConfiguredRepairBudget `json:"configured_repair_budget,omitempty"`
	Rounds                    int                     `json:"rounds"`
	RepairWindowStart         int                     `json:"repair_window_start,omitempty"`
	Attempt                   int                     `json:"attempt"`
	CheckAttempt              int                     `json:"check_attempt"`
	CandidatePhasePending     bool                    `json:"candidate_phase_pending,omitempty"`
	Instructions              string                  `json:"instructions"`
	ResumeState               string                  `json:"resume_state,omitempty"`
	PendingRole               string                  `json:"pending_role,omitempty"`
	Feedback                  string                  `json:"feedback,omitempty"`
	MissingChecksSince        time.Time               `json:"missing_checks_since,omitempty"`
	StopReason                string                  `json:"stop_reason,omitempty"`
	CI                        CIResult                `json:"ci"`
	Reconciliation            *Reconciliation         `json:"reconciliation,omitempty"`
	RestackAudit              []RestackBoundary       `json:"restack_audit,omitempty"`
	StartedAt                 time.Time               `json:"started_at"`
	UpdatedAt                 time.Time               `json:"updated_at"`
}

// Timings are observed controller intervals. They exclude time between
// controller invocations; they are not active model-compute durations.
type Timings struct {
	FormatterWallMS        int64         `json:"formatter_wall_ms,omitempty"`
	FormatterCorrections   int           `json:"formatter_corrections,omitempty"`
	Partial                bool          `json:"partial,omitempty"`
	CIWallMS               int64         `json:"ci_wall_ms,omitempty"`
	CIQueryMS              int64         `json:"ci_query_ms,omitempty"`
	CheckOperations        []CheckTiming `json:"check_operations,omitempty"`
	DroppedCheckOperations int           `json:"dropped_check_operations,omitempty"`
	Version                int           `json:"version"`
	ControllerMS           int64         `json:"controller_ms"`
	ChecksMS               int64         `json:"checks_ms"`
	CIWaitMS               int64         `json:"ci_wait_ms"`
}

func (t Timings) Recorded() *Timings {
	if t.Version != 1 && t.Version != 2 {
		return nil
	}
	return &t
}

type Revision struct {
	Head  string `json:"head"`
	Tree  string `json:"tree"`
	Clean bool   `json:"clean"`
}

type Repository interface {
	Inspect(context.Context, string) (Revision, error)
	Bundle(context.Context, string, string) error
}
