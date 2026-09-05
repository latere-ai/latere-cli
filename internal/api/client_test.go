// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIErrorPolicySidecarRequiredIsActionable(t *testing.T) {
	err := (&APIError{
		Status:  400,
		Code:    "policy_sidecar_required",
		Message: "policy requires a sidecar but client config is missing or incomplete",
	}).Error()

	for _, want := range []string{
		"cannot create cella",
		"server has no complete sidecar configuration for this CLI token",
		"not a local command syntax problem",
		"latere login",
		"latere cella policy list",
		"spec.policy",
		"sidecar is `no`",
		"server code: policy_sidecar_required",
	} {
		if !strings.Contains(err, want) {
			t.Fatalf("error missing %q:\n%s", want, err)
		}
	}
	if strings.Contains(err, "client config is missing or incomplete") {
		t.Fatalf("error leaked raw implementation message:\n%s", err)
	}
}

func TestClientErrorsPreserveHTTPStatus(t *testing.T) {
	for _, raw := range []bool{false, true} {
		for _, body := range []string{
			`{"status":200,"code":"unavailable","message":"try later","request_id":"req-test"}`,
			`{"Status":404,"code":"unavailable","message":"try later","request_id":"req-test"}`,
			`{"STATUS":0,"code":"unavailable","message":"try later","request_id":"req-test"}`,
			`{"code":"unavailable","message":"try later","request_id":"req-test"}`,
		} {
			t.Run(fmt.Sprintf("raw=%t/%s", raw, body), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, body)
				}))
				defer server.Close()
				client := &Client{BaseURL: server.URL, HTTP: server.Client()}
				var err error
				if raw {
					_, err = client.DoRaw(t.Context(), http.MethodGet, "/test", nil, "")
				} else {
					err = client.GetJSON(t.Context(), "/test", nil)
				}
				apiErr, ok := errors.AsType[*APIError](err)
				if !ok {
					t.Fatalf("expected structured HTTP error, got %v", err)
				}
				if apiErr.Status != http.StatusServiceUnavailable || apiErr.Code != "unavailable" || apiErr.Message != "try later" || apiErr.ReqID != "req-test" {
					t.Errorf("HTTP error fields = %+v", *apiErr)
				}
			})
		}
	}
}
