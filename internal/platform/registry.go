package platform

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/model"
)

var (
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9]{1,31}$`)
	slugRe = regexp.MustCompile(`^[a-z]{2,8}$`)
)

// Registry is the set of platforms this instance archives from. Platform
// values returned by Get/BySlug/All alias the registry's slices (Feeds,
// ImageHosts) and must be treated as read-only after startup.
type Registry struct {
	list   []Platform // by Priority, then Name
	byName map[string]Platform
	bySlug map[string]Platform
}

// NewRegistry validates and indexes the platforms.
func NewRegistry(ps ...Platform) (*Registry, error) {
	if len(ps) == 0 {
		return nil, errors.New("platform: empty registry")
	}
	r := &Registry{byName: map[string]Platform{}, bySlug: map[string]Platform{}}
	legacy := 0
	for _, p := range ps {
		if err := validate(p); err != nil {
			return nil, err
		}
		if _, dup := r.byName[p.Name]; dup {
			return nil, fmt.Errorf("platform: duplicate name %q", p.Name)
		}
		if _, dup := r.bySlug[p.Slug]; dup {
			return nil, fmt.Errorf("platform: duplicate slug %q", p.Slug)
		}
		if p.Legacy {
			legacy++
		}
		r.byName[p.Name], r.bySlug[p.Slug] = p, p
		r.list = append(r.list, p)
	}
	if legacy > 1 {
		return nil, errors.New("platform: more than one legacy platform")
	}
	slices.SortStableFunc(r.list, func(a, b Platform) int {
		return cmp.Or(cmp.Compare(a.Priority, b.Priority), strings.Compare(a.Name, b.Name))
	})
	return r, nil
}

func validate(p Platform) error {
	switch {
	case !nameRe.MatchString(p.Name):
		return fmt.Errorf("platform: invalid name %q", p.Name)
	case !slugRe.MatchString(p.Slug) || p.Slug == "map":
		return fmt.Errorf("platform %s: invalid slug %q", p.Name, p.Slug)
	case p.DisplayName == "":
		return fmt.Errorf("platform %s: missing display name", p.Name)
	case len(p.ReplayExt) < 2 || !strings.HasPrefix(p.ReplayExt, "."):
		return fmt.Errorf("platform %s: invalid replay extension %q", p.Name, p.ReplayExt)
	case p.Adapter == nil || p.ProfileURL == nil || p.ParseURL == nil || p.ValidID == nil:
		return fmt.Errorf("platform %s: missing adapter or ref functions", p.Name)
	}
	seen := map[string]bool{}
	for _, f := range p.Feeds {
		if f.Kind != model.KindScore && f.Kind != model.KindAttempt {
			return fmt.Errorf("platform %s: unknown feed kind %q", p.Name, f.Kind)
		}
		if seen[f.Kind] {
			return fmt.Errorf("platform %s: duplicate feed %q", p.Name, f.Kind)
		}
		seen[f.Kind] = true
		if f.NeedsAccess && !f.Optional {
			return fmt.Errorf("platform %s: feed %q needs access but is not optional", p.Name, f.Kind)
		}
	}
	if f, ok := p.Feed(model.KindScore); !ok || f.Optional {
		return fmt.Errorf("platform %s: needs a required %q feed", p.Name, model.KindScore)
	}
	return nil
}

// All returns the platforms by Priority, then name.
func (r *Registry) All() []Platform { return slices.Clone(r.list) }

func (r *Registry) Get(name string) (Platform, bool) {
	p, ok := r.byName[name]
	return p, ok
}

func (r *Registry) BySlug(slug string) (Platform, bool) {
	p, ok := r.bySlug[slug]
	return p, ok
}

// Legacy returns the legacy platform (ScoreSaber), if registered.
func (r *Registry) Legacy() (Platform, bool) {
	for _, p := range r.list {
		if p.Legacy {
			return p, true
		}
	}
	return Platform{}, false
}

// Priority orders platforms for display; unknown names sort last.
func (r *Registry) Priority(name string) int {
	if p, ok := r.byName[name]; ok {
		return p.Priority
	}
	return math.MaxInt
}

// ParseRef resolves an admin's input to a platform and account ID. With
// platformName empty, profile URLs pick their platform and bare IDs go to
// the legacy platform.
func (r *Registry) ParseRef(input, platformName string) (Platform, string, error) {
	in := strings.TrimSpace(input)
	if platformName != "" {
		p, ok := r.Get(platformName)
		if !ok {
			return Platform{}, "", fmt.Errorf("%w: unknown platform %q", ErrInvalidRef, platformName)
		}
		if id, ok := p.ParseURL(in); ok {
			return p, id, nil
		}
		if p.ValidID(in) {
			return p, in, nil
		}
		return Platform{}, "", fmt.Errorf("%w: not a %s player ID or profile URL", ErrInvalidRef, p.DisplayName)
	}
	for _, p := range r.list {
		if id, ok := p.ParseURL(in); ok {
			return p, id, nil
		}
	}
	if p, ok := r.Legacy(); ok && p.ValidID(in) {
		return p, in, nil
	}
	return Platform{}, "", fmt.Errorf("%w: paste a profile URL or a player ID", ErrInvalidRef)
}
