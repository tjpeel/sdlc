package runusage

import "testing"

func TestSyntheticClaudeQuotaEnvelopePreservesRealModelAndContext(t *testing.T) {
	o := NewObserver("claude", "selected-model")
	_, _ = o.Write([]byte(`{"type":"system","subtype":"init","model":"selected-model","session_id":"fixture-session"}` + "\n"))
	_, _ = o.Write([]byte(`{"type":"assistant","message":{"id":"work-message","model":"selected-model","usage":{"input_tokens":12,"output_tokens":3}}}` + "\n"))
	_, _ = o.Write([]byte(`{"type":"assistant","error":"rate_limit","message":{"id":"error-message","model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0}}}` + "\n"))
	u := o.Snapshot()
	if u.ReportedModel != "selected-model" || !u.ModelMatches || u.Context == nil || u.Context.InputTokens == nil || *u.Context.InputTokens != 12 {
		t.Fatal("synthetic failure replaced real model/context", u.ReportedModel, u.ModelMatches, u.Context)
	}
	if _, exists := u.Models["<synthetic>"]; exists {
		t.Fatal("synthetic error counted as model inference")
	}
}

func TestRealClaudeModelDriftSurvivesSyntheticQuotaEnvelope(t *testing.T) {
	o := NewObserver("claude", "selected-model")
	_, _ = o.Write([]byte(`{"type":"system","subtype":"init","model":"selected-model"}` + "\n"))
	_, _ = o.Write([]byte(`{"type":"assistant","message":{"id":"work-message","model":"different-model","usage":{"input_tokens":8}}}` + "\n"))
	_, _ = o.Write([]byte(`{"type":"assistant","error":"rate_limit","message":{"id":"error-message","model":"<synthetic>"}}` + "\n"))
	u := o.Snapshot()
	if u.ReportedModel != "different-model" || u.ModelMatches {
		t.Fatal("synthetic quota error hid actual model drift")
	}
}

func TestSyntheticModelOutsideQuotaEnvelopeStillFailsIdentity(t *testing.T) {
	for _, event := range []string{
		`{"type":"assistant","message":{"id":"message","model":"<synthetic>"}}`,
		`{"type":"assistant","error":"authentication_failed","message":{"id":"message","model":"<synthetic>"}}`,
		`{"type":"system","subtype":"init","model":"<synthetic>"}`,
	} {
		o := NewObserver("claude", "selected-model")
		_, _ = o.Write([]byte(event + "\n"))
		if u := o.Snapshot(); u.ModelMatches || u.ReportedModel != "<synthetic>" {
			t.Fatal("unrecognised synthetic model was ignored")
		}
	}
}

func TestSyntheticClaudeQuotaWithoutInitRetainsUnverifiedModel(t *testing.T) {
	o := NewObserver("claude", "selected-model")
	o.Consume([]byte(`{"type":"assistant","error":"rate_limit","message":{"model":"<synthetic>"}}`))
	u := o.Snapshot()
	if u.ReportedModel != "<synthetic>" || u.ModelMatches {
		t.Fatal("unverified synthetic model was suppressed", u)
	}
}
