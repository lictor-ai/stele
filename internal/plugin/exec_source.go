package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lictor-ai/stele/internal/printable"
	"github.com/lictor-ai/stele/internal/rulesblock"
	"github.com/lictor-ai/stele/internal/source"
)

// What a source's read may hold (D78).
const (
	maxRules       = 10000
	maxRuleBody    = 1 << 20
	maxFingerprint = 256
	// fingerprintTimeout is a fingerprint call's time limit: it must be cheap.
	fingerprintTimeout = 10 * time.Second
)

var (
	// metaKey is a metadata key as stele.md writes one.
	metaKey = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	// uuid is an id as stele mints one: it goes into a generated file, so
	// nothing else may (D52).
	uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// execSource is an external source: a stele-source-<name> on PATH. To
// compile it is a source like stelemd.
type execSource struct {
	name, bin string
	config    json.RawMessage
	timeout   time.Duration // read's
	run       Runner

	once sync.Once
	derr error
}

func (s *execSource) Name() string { return s.name }

// fail is err from op, naming the executable.
func (s *execSource) fail(op string, err error) error {
	return &Error{Kind: KindSource, Name: s.name, List: "sources", Exe: Exe(KindSource, s.name), Op: op, Err: err}
}

// call asks the source op, after describe once, and decodes the answer into v.
func (s *execSource) call(ctx context.Context, op string, fields map[string]any, max int, timeout time.Duration, v any) error {
	s.once.Do(func() { _, s.derr = describe(ctx, s.run, KindSource, s.name, s.bin) })
	if s.derr != nil {
		return s.derr
	}
	req, err := Request(op, fields)
	if err != nil {
		return s.fail(op, err)
	}
	out, err := s.run.Run(ctx, s.bin, req, max, timeout)
	if err != nil {
		return s.fail(op, err)
	}
	if !utf8.Valid(out) {
		return s.fail(op, fmt.Errorf("%w: answer is not valid UTF-8", ErrProtocol))
	}
	if err := DecodeAnswer(out, v); err != nil {
		return s.fail(op, err)
	}
	return nil
}

// sentRule is a rule as a source's read answers it. A position it sends is
// ignored: it is the rule's place in the list.
type sentRule struct {
	Body        *string           `json:"body"`
	File        string            `json:"file"`
	Line        int               `json:"line"`
	Description string            `json:"description"`
	Tags        []string          `json:"tags"`
	Scope       []string          `json:"scope"`
	Meta        map[string]string `json:"meta"`
	ID          string            `json:"id"`
}

func (s *execSource) Read(ctx context.Context, root string) ([]source.Rule, string, error) {
	var a struct {
		Fingerprint *string     `json:"fingerprint"`
		Rules       *[]sentRule `json:"rules"`
	}
	if err := s.call(ctx, "read", map[string]any{"root": root, "config": s.config}, LargeAnswer, s.timeout, &a); err != nil {
		return nil, "", err
	}
	fp, err := checkFingerprint(a.Fingerprint)
	if err != nil {
		return nil, "", s.fail("read", err)
	}
	if a.Rules == nil {
		return nil, "", s.fail("read", fmt.Errorf("%w: answer has no rules", ErrProtocol))
	}
	if len(*a.Rules) > maxRules {
		return nil, "", s.fail("read", fmt.Errorf("%w: %d rules, at most %d", ErrProtocol, len(*a.Rules), maxRules))
	}
	rules := make([]source.Rule, len(*a.Rules))
	for i, r := range *a.Rules {
		if err := checkRule(r); err != nil {
			return nil, "", s.fail("read", fmt.Errorf("%w: rule %d: %v", ErrProtocol, i, err))
		}
		rules[i] = source.Rule{
			Description: r.Description,
			Tags:        r.Tags,
			Scope:       r.Scope,
			Body:        *r.Body,
			Meta:        r.Meta,
			ID:          r.ID,
			Source:      s.name,
			File:        r.File,
			Position:    i,
			Line:        r.Line,
		}
	}
	return rules, fp, nil
}

func (s *execSource) Fingerprint(ctx context.Context, root string) (string, error) {
	var a struct {
		Fingerprint *string `json:"fingerprint"`
	}
	if err := s.call(ctx, "fingerprint", map[string]any{"root": root, "config": s.config}, SmallAnswer, fingerprintTimeout, &a); err != nil {
		return "", err
	}
	fp, err := checkFingerprint(a.Fingerprint)
	if err != nil {
		return "", s.fail("fingerprint", err)
	}
	return fp, nil
}

func checkFingerprint(fp *string) (string, error) {
	if fp == nil || *fp == "" || len(*fp) > maxFingerprint || printable.Clean(*fp) != *fp {
		return "", fmt.Errorf("%w: fingerprint is 1 to %d printable bytes", ErrProtocol, maxFingerprint)
	}
	return *fp, nil
}

// checkRule reports what in r stele will not take from a source.
func checkRule(r sentRule) error {
	if r.Body == nil || strings.TrimSpace(*r.Body) == "" {
		return fmt.Errorf("no body")
	}
	body := *r.Body
	switch {
	case len(body) > maxRuleBody:
		return fmt.Errorf("body over %d bytes", maxRuleBody)
	case strings.ContainsRune(body, 0):
		return fmt.Errorf("body holds a NUL")
	}
	for _, line := range strings.Split(body, "\n") {
		if rulesblock.IsMarker(line) {
			return fmt.Errorf("body holds a line that marks a rule in a generated file")
		}
	}
	if r.ID != "" && !uuid.MatchString(r.ID) {
		return fmt.Errorf("id %q is not a lower-case UUID", printable.Clean(r.ID))
	}
	for k, v := range r.Meta {
		if !metaKey.MatchString(k) {
			return fmt.Errorf("meta key %q is not lower-case letters, digits, _ and -", printable.Clean(k))
		}
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("meta %q is more than one line", k)
		}
	}
	if r.File != "" {
		if strings.Contains(r.File, `\`) || path.IsAbs(r.File) || path.Clean(r.File) != r.File ||
			r.File == ".." || strings.HasPrefix(r.File, "../") || printable.Clean(r.File) != r.File {
			return fmt.Errorf("file %q is not a relative path with / separators inside the project", printable.Clean(r.File))
		}
	}
	return nil
}
