package api

import (
	"testing"

	"ctlvps/internal/domain"
)

func TestShareInputQuotaModesAndOmittedEdit(t *testing.T) {
	for _, mode := range []string{domain.BillingDual, domain.BillingUp, domain.BillingDown, domain.BillingMax} {
		t.Run(mode, func(t *testing.T) {
			sh := domain.Share{}
			if err := (shareInput{Name: "fixture", BillingMode: mode}).apply(&sh); err != nil || sh.BillingMode != mode {
				t.Fatal("valid share quota mode not preserved", sh.BillingMode, err)
			}
			sh.ID = 1
			if err := (shareInput{Name: "edited"}).apply(&sh); err != nil || sh.BillingMode != mode {
				t.Fatal("old client editing without a mode changed existing accounting", sh.BillingMode, err)
			}
		})
	}
	for _, mode := range []string{"", domain.BillingSum} {
		sh := domain.Share{}
		if err := (shareInput{Name: "fixture", BillingMode: mode}).apply(&sh); err != nil || sh.BillingMode != domain.BillingDual {
			t.Fatal("legacy/default share mode changed", sh.BillingMode, err)
		}
	}
	if err := (shareInput{Name: "fixture", BillingMode: "both"}).apply(&domain.Share{}); err == nil {
		t.Fatal("unknown share mode accepted")
	}
}
