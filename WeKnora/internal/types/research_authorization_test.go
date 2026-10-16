package types

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestResearchAuthorizationIsOpaqueAndRejectsInvalidHeaders(t *testing.T) {
	for _, invalid := range []string{"", "Basic abc", "Bearer ", "Bearer x\r\nOther: y"} {
		ctx := WithResearchAuthorization(context.Background(), invalid)
		if ResearchAuthorizationFromContext(ctx) != "" {
			t.Fatal("invalid authorization accepted")
		}
	}
	ctx := WithResearchAuthorization(context.Background(), "Bearer synthetic-token")
	if ResearchAuthorizationFromContext(ctx) != "Bearer synthetic-token" {
		t.Fatal("authorization missing")
	}
	value := ctx.Value(ResearchAuthorizationContextKey)
	if fmt.Sprint(value) != "[redacted]" {
		t.Fatal("context diagnostics exposed authorization")
	}
	raw, err := json.Marshal(value)
	if err != nil || string(raw) != "{}" {
		t.Fatal("generic JSON exposed authorization")
	}
}
