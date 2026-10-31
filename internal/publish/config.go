package publish

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/lictor-ai/stele/internal/enforce"
	"github.com/lictor-ai/stele/internal/safefile"
)

var (
	// ErrNoConfig is a stele.json with no "publish" key, or no stele.json.
	ErrNoConfig = errors.New(`no "publish" key in stele.json`)
	// ErrBadConfig is a "repo" or "path" stele refuses. stele.json is
	// committed, so a cloned repository chooses them.
	ErrBadConfig = errors.New("bad publish setting in stele.json")
)

// DefaultPath is where stele.md goes in the repository when "path" is unset.
const DefaultPath = "stele.md"

// example is the key to add, shown when it is missing.
const example = `{"publish": {"repo": "git@github.com:me/guidelines.git", "path": "project-a/stele.md"}}`

// maxConfig caps stele.json.
const maxConfig = 1 << 20

// maxPath caps "path".
const maxPath = 200

// Config is the "publish" key of stele.json. stele reads it and never
// writes it (D38).
type Config struct {
	Repo string // the repository to publish to
	Path string // where stele.md goes inside it
}

// LoadConfig reads the "publish" key of stele.json under root and checks it.
func LoadConfig(root string) (Config, error) {
	data, err := safefile.ReadRegular(filepath.Join(root, enforce.FileName), maxConfig)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, fmt.Errorf("%w; add one, for example: %s", ErrNoConfig, example)
	}
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", enforce.FileName, err)
	}
	var raw struct {
		Publish *struct {
			Repo string `json:"repo"`
			Path string `json:"path"`
		} `json:"publish"`
	}
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), &raw); err != nil {
		return Config{}, fmt.Errorf("%s: %w", enforce.FileName, err)
	}
	if raw.Publish == nil {
		return Config{}, fmt.Errorf("%w; add one, for example: %s", ErrNoConfig, example)
	}
	cfg := Config{Repo: raw.Publish.Repo, Path: raw.Publish.Path}
	if cfg.Path == "" {
		cfg.Path = DefaultPath
	}
	if err := validateRepo(cfg.Repo); err != nil {
		return Config{}, err
	}
	if err := validatePath(cfg.Path); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

var (
	hostname = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)
	port     = regexp.MustCompile(`^[0-9]{1,5}$`)
	// scp is git's user@host:path form. The user is required, so a local
	// path such as C:/x is never read as one.
	scp = regexp.MustCompile(`^([A-Za-z0-9._-]+)@([A-Za-z0-9][A-Za-z0-9.-]*):([^/:\-].*)$`)
)

// validateRepo accepts https://host/path, ssh://host/path and user@host:path,
// and nothing else: no local path, no file:// or ext::, no option, no
// password.
func validateRepo(repo string) error {
	bad := func(why string) error {
		return fmt.Errorf("%w: repo %q: %s; use https://host/path, ssh://host/path or user@host:path", ErrBadConfig, repo, why)
	}
	if repo == "" {
		return fmt.Errorf("%w: repo is empty; use https://host/path, ssh://host/path or user@host:path", ErrBadConfig)
	}
	for _, r := range repo {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("%w: repo has a space or a control character", ErrBadConfig)
		}
	}
	if strings.HasPrefix(repo, "-") {
		return bad("it starts with -")
	}
	if !strings.Contains(repo, "://") {
		if at := strings.Index(repo, "@"); at > 0 && strings.Contains(repo[:at], ":") {
			return fmt.Errorf("%w: repo holds a password; remove it and let git's own login supply it", ErrBadConfig)
		}
		m := scp.FindStringSubmatch(repo)
		if m == nil {
			return bad("not a remote URL")
		}
		return nil
	}
	u, err := url.Parse(repo)
	if err != nil {
		if strings.Contains(repo, "@") {
			return fmt.Errorf("%w: repo is not a valid URL", ErrBadConfig)
		}
		return bad("not a valid URL")
	}
	if _, has := u.User.Password(); has {
		return fmt.Errorf("%w: repo holds a password; remove it and let git's own login supply it", ErrBadConfig)
	}
	if u.Scheme != "https" && u.Scheme != "ssh" {
		return bad("only https and ssh are allowed")
	}
	if !hostname.MatchString(u.Hostname()) {
		return bad("no valid host")
	}
	if p := u.Port(); p != "" && !port.MatchString(p) {
		return bad("bad port")
	}
	if u.Path == "" || u.Path == "/" {
		return bad("no repository path")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return bad("a query or fragment")
	}
	return nil
}

var pathPart = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// validatePath accepts a relative path made of letters, digits, '.', '_' and
// '-', ending in .md, that is also a valid branch name under stele/publish/.
func validatePath(p string) error {
	bad := func(why string) error {
		return fmt.Errorf("%w: path %q: %s", ErrBadConfig, p, why)
	}
	switch {
	case p == "":
		return bad("empty")
	case len(p) > maxPath:
		return bad(fmt.Sprintf("over %d bytes", maxPath))
	case !strings.HasSuffix(p, ".md"):
		return bad("must end in .md")
	}
	for _, part := range strings.Split(p, "/") {
		switch {
		case !pathPart.MatchString(part):
			return bad("each part is letters, digits, '.', '_' and '-', and starts with a letter, a digit or '_'")
		case strings.Contains(part, ".."):
			return bad("'..' is not allowed")
		case strings.HasSuffix(part, "."), strings.HasSuffix(part, ".lock"):
			return bad("a part ends with '.' or '.lock'")
		}
	}
	return nil
}
