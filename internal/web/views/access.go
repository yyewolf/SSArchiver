package views

import (
	"context"
	"fmt"

	"github.com/a-h/templ"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
)

// FeedState is one optional feed of an account: its declaration, its row
// (zero when it was never switched on) and its counts.
type FeedState struct {
	Spec   platform.FeedSpec
	Feed   model.SyncFeed
	Counts service.Counts
}

func (f FeedState) On() bool      { return f.Feed.Enabled }
func (f FeedState) Private() bool { return f.Feed.Enabled && f.Feed.Access == model.AccessPrivate }

// OptionalFeeds lists the optional feeds of an account's platform.
func OptionalFeeds(ctx context.Context, id service.Identity) []FeedState {
	reg := Platforms(ctx)
	if reg == nil {
		return nil
	}
	p, ok := reg.Get(id.Platform)
	if !ok {
		return nil
	}
	var out []FeedState
	for _, spec := range p.Feeds {
		if spec.Optional {
			out = append(out, FeedState{Spec: spec, Feed: id.Feed(spec.Kind), Counts: id.Counts[spec.Kind]})
		}
	}
	return out
}

// FeedSpecOf is a feed's declaration (zero when unknown).
func FeedSpecOf(ctx context.Context, platformName, kind string) platform.FeedSpec {
	if reg := Platforms(ctx); reg != nil {
		if p, ok := reg.Get(platformName); ok {
			f, _ := p.Feed(kind)
			return f
		}
	}
	return platform.FeedSpec{}
}

// hasPrivateFeed reports whether the platform refuses one of an account's switched-on feeds.
func hasPrivateFeed(id service.Identity) bool {
	for _, f := range id.Feeds {
		if f.Enabled && f.Access == model.AccessPrivate {
			return true
		}
	}
	return false
}

// FeedName is a feed's plural noun: "attempts".
func FeedName(kind string) string { return kind + "s" }

func FeedURL(playerID, platformName, kind string) string {
	return "/admin/players/" + playerID + "/identities/" + platformName + "/feeds/" + kind
}

// feedConfirm asks before switching a feed on: it downloads a whole history.
func feedConfirm(ctx context.Context, pl service.PlayerSummary, id service.Identity, fs FeedState) templ.Attributes {
	if fs.On() {
		return templ.Attributes{}
	}
	name := PlatformName(ctx, id.Platform)
	msg := fmt.Sprintf("Archive every %s %s of %s? This history can reach several GB of replays.", name, fs.Spec.Kind, pl.Name)
	if fs.Feed.RemoteTotal > 0 {
		msg += fmt.Sprintf(" %s reported %s %s at the last check.", name, Number(fs.Feed.RemoteTotal), FeedName(fs.Spec.Kind))
	}
	return templ.Attributes{"hx-confirm": msg}
}

// feedStatus is the one-line state under a switched-on feed: "attempts: public · 12 archived".
func feedStatus(fs FeedState) string {
	access := map[string]string{
		model.AccessPublic: "public", model.AccessPrivate: "private", model.AccessUnknown: "checking access", model.AccessNA: "on",
	}[fs.Feed.Access]
	return fmt.Sprintf("%s: %s · %s archived", FeedName(fs.Spec.Kind), access, Number(fs.Counts.Archived))
}
