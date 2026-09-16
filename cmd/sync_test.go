package cmd

import (
	"testing"

	"github.com/nogo/herald/internal/maintenance"
)

func TestSyncExitError(t *testing.T) {
	t.Run("no failures exits clean", func(t *testing.T) {
		rep := &maintenance.Report{
			Stacks: []maintenance.StackReport{
				{Name: "a", Action: "redeployed"},
				{Name: "b", Action: "none"},
			},
		}
		if err := syncExitError(rep); err != nil {
			t.Errorf("got error %v, want nil", err)
		}
	})

	t.Run("a failed deploy exits nonzero", func(t *testing.T) {
		rep := &maintenance.Report{
			Stacks: []maintenance.StackReport{
				{Name: "a", Action: "redeployed"},
				{Name: "b", Action: "deploy failed", Detail: "boom"},
			},
		}
		if err := syncExitError(rep); err == nil {
			t.Error("expected a nonzero-exit error when a stack failed to deploy")
		}
	})

	t.Run("a queued async deploy does not exit nonzero", func(t *testing.T) {
		rep := &maintenance.Report{
			Stacks: []maintenance.StackReport{
				{Name: "a", Action: "deploy queued"},
			},
		}
		if err := syncExitError(rep); err != nil {
			t.Errorf("got error %v, want nil — a pending result is not a failure", err)
		}
	})
}
