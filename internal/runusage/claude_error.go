package runusage

// ClaudeSyntheticQuotaError recognises the observed native assistant error
// envelope, rather than an inference performed by another model. Independent
// native rejection and safe wrapper finalization still govern quota handling.
func ClaudeSyntheticQuotaError(provider, eventType, errorCode, messageModel string) bool {
	return provider == "claude" && eventType == "assistant" && errorCode == "rate_limit" && messageModel == "<synthetic>"
}
