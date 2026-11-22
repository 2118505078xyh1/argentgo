package security

import "testing"

func TestPromptApproverDeclaresExplicitYes(t *testing.T) {
	var p *PromptApprover
	if !p.RequiresExplicitYes() {
		t.Fatal("PromptApprover must require explicit yes so command-risk guard fires in production")
	}
	var _ ExplicitYesApprover = (*PromptApprover)(nil)
}
