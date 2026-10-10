package platform

import (
	"net/url"
	"slices"

	"github.com/yyewolf/ssarchiver/internal/model"
)

// PlayRef locates one stored play for public URLs (spec §6.1).
type PlayRef struct{ Platform, Kind, ExternalID string }

// PlayPath is a play's public path under prefix ("/s" score page, "/embed"
// viewer, "/r" raw file without extension): /s/{id} for the legacy
// platform's scores, else /s/{slug}/{id} or /s/{slug}/attempt/{id}.
// It is "" for an unknown platform.
func (r *Registry) PlayPath(prefix string, ref PlayRef) string {
	p, ok := r.Get(ref.Platform)
	if !ok {
		return ""
	}
	id := url.PathEscape(ref.ExternalID)
	switch {
	case p.Legacy && ref.Kind == model.KindScore:
		return prefix + "/" + id
	case ref.Kind == model.KindAttempt:
		return prefix + "/" + p.Slug + "/attempt/" + id
	}
	return prefix + "/" + p.Slug + "/" + id
}

// ReplayPath is the raw replay file's path: /r/{id}.dat for legacy scores,
// else /r/{slug}/[attempt/]{id}{ext}.
func (r *Registry) ReplayPath(ref PlayRef) string {
	p, ok := r.Get(ref.Platform)
	if !ok {
		return ""
	}
	return r.PlayPath("/r", ref) + p.ReplayExt
}

// AccountPath is the readable link to a tracked account, /p/{slug}/{id};
// it survives merges (spec §6.1).
func (r *Registry) AccountPath(platformName, externalID string) string {
	p, ok := r.Get(platformName)
	if !ok {
		return ""
	}
	return "/p/" + p.Slug + "/" + url.PathEscape(externalID)
}

// ImageHosts are every platform's image hosts (registry order, deduplicated),
// for the CSP img-src.
func (r *Registry) ImageHosts() []string {
	var out []string
	for _, p := range r.list {
		for _, h := range p.ImageHosts {
			if !slices.Contains(out, h) {
				out = append(out, h)
			}
		}
	}
	return out
}
