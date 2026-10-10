package views

import (
	"context"
	"encoding/json"
	"math"
	"net/url"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
)

// beatLeaderViewerURL is BeatLeader's hosted web replay viewer
// (BeatSaber-Web-Replays); it sends no framing restrictions.
const beatLeaderViewerURL = "https://replay.beatleader.com"

type platformsKey struct{}

// WithPlatforms stores the registry for components that build platform
// URLs; the web middleware does it for every request.
func WithPlatforms(ctx context.Context, reg *platform.Registry) context.Context {
	return context.WithValue(ctx, platformsKey{}, reg)
}

// Platforms is the request's registry (nil outside a request).
func Platforms(ctx context.Context) *platform.Registry {
	reg, _ := ctx.Value(platformsKey{}).(*platform.Registry)
	return reg
}

func playRef(s *model.Score) platform.PlayRef {
	return platform.PlayRef{Platform: s.Platform, Kind: s.Kind, ExternalID: s.ExternalID}
}

// ScoreURL is a row's public page ("" without a registry).
func ScoreURL(ctx context.Context, s *model.Score) string {
	if reg := Platforms(ctx); reg != nil {
		return reg.PlayPath("/s", playRef(s))
	}
	return ""
}

// ReplayPath is a row's raw replay file.
func ReplayPath(ctx context.Context, s *model.Score) string {
	if reg := Platforms(ctx); reg != nil {
		return reg.ReplayPath(playRef(s))
	}
	return ""
}

// EmbedPath is a row's embeddable viewer page.
func EmbedPath(ctx context.Context, s *model.Score) string {
	if reg := Platforms(ctx); reg != nil {
		return reg.PlayPath("/embed", playRef(s))
	}
	return ""
}

// ReplayExt is the replay file extension of a row's platform (".dat", ".bsor").
func ReplayExt(ctx context.Context, s *model.Score) string {
	if reg := Platforms(ctx); reg != nil {
		if p, ok := reg.Get(s.Platform); ok {
			return p.ReplayExt
		}
	}
	return ""
}

// AccountPath is the shareable /p/{slug}/{externalID} link of an account.
func AccountPath(ctx context.Context, platformName, externalID string) string {
	if reg := Platforms(ctx); reg != nil {
		return reg.AccountPath(platformName, externalID)
	}
	return ""
}

// ProfileURL is an account's profile on its platform.
func ProfileURL(ctx context.Context, platformName, externalID string) string {
	if reg := Platforms(ctx); reg != nil {
		if p, ok := reg.Get(platformName); ok {
			return p.ProfileURL(externalID)
		}
	}
	return ""
}

// PlatformName is a platform's display name (its stored name when unknown).
func PlatformName(ctx context.Context, platformName string) string {
	if reg := Platforms(ctx); reg != nil {
		if p, ok := reg.Get(platformName); ok {
			return p.DisplayName
		}
	}
	return platformName
}

// ViewerSrc is the same-origin ArcViewer URL that loads one archived replay.
// When the instance enables headset rendering, ArcViewer's settingsOverride
// parameter applies the admin's choices over each visitor's own viewer
// settings (the visitor can still veto them inside the viewer UI).
func ViewerSrc(ctx context.Context, base string, s *model.Score, autoplay, loop, hideUI bool, st service.Settings) string {
	v := url.Values{}
	v.Set("replayURL", base+ReplayPath(ctx, s))
	v.Set("noProxy", "true")
	if autoplay {
		v.Set("autoPlay", "true")
	}
	if loop {
		v.Set("loop", "true")
	}
	if hideUI {
		v.Set("uiOff", "true")
	}
	if st.ViewerShowHeadset {
		r, g, b, ok := service.ParseHexColor(st.ViewerHeadsetColor)
		if !ok {
			r, g, b = 0.529, 0.529, 0.529
		}
		round := func(f float64) float64 { return math.Round(f*1000) / 1000 }
		override := struct {
			Bools  map[string]bool    `json:"Bools"`
			Ints   map[string]int     `json:"Ints"`
			Floats map[string]float64 `json:"Floats"`
		}{
			// All three dictionaries must be present (empty is fine):
			// Newtonsoft leaves omitted ones null and ArcViewer dereferences
			// them when overrides are active.
			Bools: map[string]bool{"showheadset": true},
			Ints:  map[string]int{},
			Floats: map[string]float64{
				"headsetalpha":   round(st.ViewerHeadsetAlpha),
				"headsetcolor.r": round(r),
				"headsetcolor.g": round(g),
				"headsetcolor.b": round(b),
			},
		}
		if data, err := json.Marshal(override); err == nil {
			v.Set("settingsOverride", string(data))
		}
	}
	return "/viewer/?" + v.Encode()
}

// BeatLeaderPlays reports whether BeatLeader's viewer can load a row's replay:
// open-replay platforms only (its link loader wants a .bsor name).
func BeatLeaderPlays(ctx context.Context, s *model.Score) bool {
	if reg := Platforms(ctx); reg != nil {
		if p, ok := reg.Get(s.Platform); ok {
			return p.BSOR
		}
	}
	return false
}

// BeatLeaderSrc is the hosted BeatLeader viewer URL that plays one archived
// replay. The replay link is absolute and ends in .bsor (the viewer rejects
// other extensions); the map itself is resolved from BeatSaver by the hash
// inside the replay file.
func BeatLeaderSrc(ctx context.Context, base string, s *model.Score, autoplay, loop bool) string {
	v := url.Values{}
	v.Set("link", base+bsorReplayPath(ctx, s))
	if autoplay {
		v.Set("autoplay", "true")
	}
	if loop {
		v.Set("loop", "true")
	}
	return beatLeaderViewerURL + "/?" + v.Encode()
}

// bsorReplayPath is a row's replay under the .bsor name the BeatLeader viewer
// accepts (same bytes as ReplayPath: /r/…/{id}.bsor).
func bsorReplayPath(ctx context.Context, s *model.Score) string {
	if reg := Platforms(ctx); reg != nil {
		if p, ok := reg.Get(s.Platform); ok {
			return strings.TrimSuffix(reg.ReplayPath(playRef(s)), p.ReplayExt) + ".bsor"
		}
	}
	return ""
}
