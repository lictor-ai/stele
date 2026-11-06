package check

import (
	"context"
	"errors"
	"testing"

	"github.com/lictor-ai/stele/internal/index"
)

// countingFake counts its Validate calls.
type countingFake struct {
	fake
	validates int
}

func (f *countingFake) Validate(ctx context.Context, rules []Rule) ([]Problem, error) {
	f.validates++
	return f.fake.Validate(ctx, rules)
}

// A checker that cannot validate, such as an external one that crashed,
// stops the run: never a pass, never a problem with the rule.
func TestRunValidateError(t *testing.T) {
	boom := errors.New("stele-checker-ext validate: exited with status 3")
	f := &fake{name: "ext", validateErr: boom}
	idx := index.Index{Rules: []index.Entry{entry("r", "Run ext.", nil, map[string]string{"check": "ext"})}}
	if _, err := Run(context.Background(), t.TempDir(), idx, []string{"a.go"}, map[string]Checker{"ext": f}); !errors.Is(err, boom) {
		t.Errorf("Run = %v, want the validate error", err)
	}
	if _, err := Validate(context.Background(), idx, map[string]Checker{"ext": f}); !errors.Is(err, boom) {
		t.Errorf("Validate = %v, want the validate error", err)
	}
}

// The check after a write asks only ContentCheckers to validate: any other,
// an external one, would start a process on the hook's path (D45).
func TestRunFileValidatesOnlyContentCheckers(t *testing.T) {
	ext := &countingFake{fake: fake{name: "ext", validateErr: errors.New("must not be asked")}}
	idx := index.Index{Rules: []index.Entry{entry("r", "Run ext.", nil, map[string]string{"check": "ext"})}}
	r, ids := RunFileChecked(idx, "a.go", []byte("x"), map[string]Checker{"ext": ext}, nil)
	if ext.validates != 0 {
		t.Errorf("Validate called %d times", ext.validates)
	}
	if len(r.Problems) != 0 || len(r.Violations) != 0 || len(ids) != 0 {
		t.Errorf("RunFileChecked = %+v, %v; want nothing", r, ids)
	}
}
