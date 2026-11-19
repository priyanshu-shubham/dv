package gateway

import (
	"net/http"
	"regexp"
	"strings"
)

var (
	// The model's context overflowed: worded so, Claude Code compacts and
	// tries again rather than ending the turn.
	tooLong = regexp.MustCompile(`(?i)context_length_exceeded|context_window_exceeded|prompt is too long|input is too long|(exceeds?|exceeded|over|beyond)( the)?( model'?s?)?( maximum)? context|context (length|limit|window) (exceeded|is exceeded)|maximum context length|too many (input |prompt )?tokens|longer than the model'?s? context length|input token count .{0,40}exceeds the maximum`)
	// Reasoning another sign-in sealed, which this one can't read.
	foreignReasoning = regexp.MustCompile(`(?i)invalid_encrypted_content|invalid[ _]encrypted[ _](summary|content)|encrypted[ _]content.{0,80}could not be (verified|decrypted)|could not (decrypt|verify).{0,40}encrypted[ _]content`)
	// About the request, so it fails the same however often it is tried.
	protectionWords = regexp.MustCompile(`(?i)response protection is unavailable`)
)

// apiError is a failure as Anthropic's API words one: its HTTP status and
// error type are what Claude Code decides to retry or compact by.
type apiError struct {
	status  int
	kind    string
	message string
	noRetry bool
}

// classify words a Codex failure for Claude Code, from the backend's code
// and message and, where it answered with one, its status.
func classify(status int, code, msg string) apiError {
	return codexFailures.classify(status, code, msg)
}

// failures is how one upstream's failures are worded: whose they are, and
// what to do about a refused sign-in.
type failures struct{ who, signIn string }

var (
	codexFailures  = failures{"Codex", "try `codex login`"}
	geminiFailures = failures{"Gemini", "check GEMINI_API_KEY in Settings, Secrets"}
)

func (f failures) classify(status int, code, msg string) apiError {
	text := strings.TrimSpace(msg)
	if text == "" {
		text = code
	}
	text = f.who + ": " + text
	switch {
	case tooLong.MatchString(code) || tooLong.MatchString(msg):
		return apiError{http.StatusBadRequest, "invalid_request_error", "prompt is too long: " + text, true}
	case code == "usage_limit_reached" || code == "usage_not_included" || code == "quota_exceeded" || code == "payment_required" || status == http.StatusPaymentRequired:
		return apiError{http.StatusTooManyRequests, "rate_limit_error", text, true}
	case strings.Contains(code, "overloaded") || code == "service_unavailable" || status == 529 || status == http.StatusServiceUnavailable:
		return apiError{529, "overloaded_error", text, false}
	case code == "rate_limit_exceeded" || code == "too_many_requests" || status == http.StatusTooManyRequests:
		return apiError{http.StatusTooManyRequests, "rate_limit_error", text, false}
	case protectionWords.MatchString(msg) || code == "invalid_request_error" || foreignReasoning.MatchString(msg):
		return apiError{http.StatusBadRequest, "invalid_request_error", text, true}
	case code == "authentication" || code == "permission_denied" || status == http.StatusUnauthorized || status == http.StatusForbidden:
		return apiError{http.StatusForbidden, "permission_error", text + " (" + f.signIn + ")", true}
	case status == http.StatusNotFound:
		return apiError{http.StatusNotFound, "not_found_error", text, true}
	case status >= 500 || status == 0:
		return apiError{http.StatusInternalServerError, "api_error", text, false}
	}
	return apiError{http.StatusBadRequest, "invalid_request_error", text, true}
}

func (e apiError) write(w http.ResponseWriter) {
	if e.noRetry {
		w.Header().Set("x-should-retry", "false")
	}
	writeError(w, e.status, e.kind, e.message)
}

func (e apiError) event() event { return errorEvent(e.kind, e.message) }
