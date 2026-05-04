package api

import (
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
		"latere auth login",
		"latere cella policy list",
		"latere cella create --policy <name>",
		"SIDECAR is `no`",
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
