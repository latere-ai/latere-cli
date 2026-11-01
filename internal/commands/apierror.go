// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"latere.ai/x/sdk-go/latere"

	"github.com/latere-ai/latere-cli/internal/api"
)

// The exit codes of the commands that read the API through the SDK, the
// evals commands (spec 015, after Examen spec 009's release check), the
// oversight commands and the hosted-session commands of agents (spec
// 024): 0 is done, and a failed command says which of these it is, so a
// script or a coding agent can tell a failed result from a refusal from
// an outage.
const (
	// exitFailure is a result that is a failure, such as a watched
	// experiment that was canceled.
	exitFailure = 1
	// exitRefused is a refusal because of the request: the same command
	// gets the same answer.
	exitRefused = 2
	// exitNoAnswer is no answer yet: the service did not answer or was
	// busy, or a wait ran out. The same command may succeed later.
	exitNoAnswer = 3
)

// exitError is a command's error with the exit code HandleExitError gives
// the process.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// refusedf is a refusal the command makes itself, before or after asking
// the API, for exit code 2.
func refusedf(format string, a ...any) error {
	return &exitError{code: exitRefused, err: fmt.Errorf(format, a...)}
}

// apiRefused is what a command says of a call to name's API at base that
// failed, with its exit code: the API's refusal in its code, sentence and
// the details a person can act on; a bearer that could not be had; an API
// that could not be reached; an answer that could not be read. name is
// how a person knows the product, such as "Evals".
func apiRefused(name, base string, err error) error {
	if e, ok := errors.AsType[*latere.TokenError](err); ok {
		code := exitRefused
		if retryable(e.Err) {
			code = exitNoAnswer
		}
		return &exitError{code: code, err: fmt.Errorf("cannot authenticate to %s: %w", name, e.Err)}
	}
	if e, ok := errors.AsType[*latere.Error](err); ok {
		code := exitRefused
		if retryableStatus(e.StatusCode) {
			code = exitNoAnswer
		}
		return &exitError{code: code, err: apiRefusal(name, e.StatusCode, e.Code, api.RefusalText(e), e.Details)}
	}
	if errors.Is(err, context.DeadlineExceeded) || retryable(err) {
		return &exitError{code: exitNoAnswer, err: fmt.Errorf("reach the %s API at %s: %w", name, base, err)}
	}
	if e := jsonError(err); e != nil {
		return &exitError{code: exitNoAnswer, err: fmt.Errorf("read the %s API's answer: %w", name, e)}
	}
	return err
}

// retryableStatus reports whether an answer with status may change when the
// same request is sent again: a timeout, a rate limit or a server error.
func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

// retryable reports whether err is a failure the same request may not meet
// again: a network failure, or an answer retryableStatus names.
func retryable(err error) bool {
	if e, ok := errors.AsType[*latere.Error](err); ok {
		return retryableStatus(e.StatusCode)
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	_, ok := errors.AsType[*url.Error](err)
	return ok
}

// apiRefusal is the sentence of a refusal by name's API: "<code>:
// <message>", then the members of details a person can act on, one per
// line, and for a 401 the command that signs in again.
func apiRefusal(name string, status int, code, message string, details map[string]any) error {
	var b strings.Builder
	switch {
	case code != "" && message != "":
		b.WriteString(code + ": " + message)
	case code != "":
		b.WriteString(code)
	case message != "":
		b.WriteString(message)
	case status != 0:
		fmt.Fprintf(&b, "the %s API answered HTTP %d", name, status)
	default:
		fmt.Fprintf(&b, "the %s API refused without saying why", name)
	}
	for _, key := range []string{"field", "reason", "constant", "figure"} {
		switch v := details[key].(type) {
		case string:
			if v != "" {
				fmt.Fprintf(&b, "\n%s: %s", key, v)
			}
		case float64:
			fmt.Fprintf(&b, "\n%s: %s", key, strconv.FormatFloat(v, 'g', -1, 64))
		}
	}
	if status == http.StatusUnauthorized {
		b.WriteString("\nRun `latere login` and try again.")
	}
	return errors.New(stripANSI(b.String()))
}
