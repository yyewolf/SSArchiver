package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/components/toast"
	"github.com/yyewolf/ssarchiver/internal/web/views"
)

// sentence turns an error into a UI sentence: capitalised, ending with a period.
func sentence(err error) string {
	msg := err.Error()
	if msg == "" {
		return ""
	}
	r := []rune(msg)
	r[0] = unicode.ToUpper(r[0])
	msg = string(r)
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	return msg
}

func (h *Handler) adminPlayersView(r *http.Request) (views.AdminPlayersView, error) {
	players, err := h.svc.ListPlayers(r.Context(), true)
	return views.AdminPlayersView{Players: players, Platforms: h.svc.Platforms().All(), Now: h.svc.Now()}, err
}

// renderRow re-renders one player's row with a toast.
func (h *Handler) renderRow(w http.ResponseWriter, r *http.Request, id string, t toast.Type, title string) {
	v, err := h.adminPlayersView(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	pl, err := h.playerSummary(r, id)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.PlayerRowToast(pl, v, t, title))
}

// accessToast says what an access check found.
func accessToast(f *model.SyncFeed, okTitle string) (toast.Type, string) {
	switch {
	case f.Access == model.AccessPrivate:
		return toast.TypeWarning, "Access is private"
	case f.LastError != "":
		return toast.TypeError, "Could not check access"
	}
	return toast.TypeSuccess, okTitle
}

// rateLimited says whether an access check was throttled: the platform
// answered nothing, so the UI must not claim success.
func rateLimited(err error) bool { return errors.Is(err, platform.ErrRateLimited) }

func feedKey(r *http.Request) service.FeedKey {
	return service.FeedKey{PlayerID: r.PathValue("id"), Platform: r.PathValue("platform"), Kind: r.PathValue("kind")}
}

func (h *Handler) setFeedEnabled(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	ctx := r.Context()
	k := feedKey(r)
	enabled := r.PostFormValue("enabled") == "true"
	f, err := h.svc.SetFeedEnabled(ctx, k, enabled)
	switch {
	case errors.Is(err, service.ErrFeedNotOptional):
		h.toastOnly(w, r, toast.TypeError, "Could not change feed", sentence(err))
		return
	case isNotFound(err):
		h.toastOnly(w, r, toast.TypeError, "Account not found", "")
		return
	case err != nil:
		h.serverError(w, r, err)
		return
	}
	name := views.FeedName(k.Kind)
	if !enabled {
		h.renderRow(w, r, k.PlayerID, toast.TypeSuccess, "Stopped archiving "+name)
		return
	}
	checked, perr := h.svc.CheckFeedAccess(ctx, k)
	if checked != nil { // a failed probe is recorded on the feed
		f = checked
	}
	t, title := accessToast(f, "Archiving "+name)
	if rateLimited(perr) { // nothing was verified: the warning wins over the switch
		t, title = toast.TypeWarning, "Check again later"
	}
	h.renderRow(w, r, k.PlayerID, t, title)
}

// setFeedDownload pauses or resumes one feed's replay downloads; polling and
// backfill keep running.
func (h *Handler) setFeedDownload(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	k := feedKey(r)
	download := r.PostFormValue("enabled") == "true"
	if _, err := h.svc.SetFeedDownload(r.Context(), k, download); err != nil {
		if isNotFound(err) {
			h.toastOnly(w, r, toast.TypeError, "Feed not found", "")
			return
		}
		h.serverError(w, r, err)
		return
	}
	title := "Resumed replay downloads"
	if !download {
		title = "Paused replay downloads"
	}
	h.renderRow(w, r, k.PlayerID, toast.TypeSuccess, title)
}

func (h *Handler) checkFeedAccess(w http.ResponseWriter, r *http.Request) {
	k := feedKey(r)
	f, err := h.svc.CheckFeedAccess(r.Context(), k)
	if f == nil {
		if isNotFound(err) {
			h.toastOnly(w, r, toast.TypeError, "Feed not found", "")
			return
		}
		h.serverError(w, r, err)
		return
	}
	t, title := accessToast(f, "Access granted")
	if rateLimited(err) { // nothing was verified: do not claim success
		t, title = toast.TypeWarning, "Check again later"
	}
	if r.URL.Query().Get("from") == "sync" { // the live panel refreshes on its own
		h.toastOnly(w, r, t, title, f.LastError)
		return
	}
	h.renderRow(w, r, k.PlayerID, t, title)
}

func (h *Handler) adminPlayers(w http.ResponseWriter, r *http.Request) {
	v, err := h.adminPlayersView(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.AdminPlayersPage(h.page(r, "Manage players"), v))
}

func (h *Handler) lookupPlayer(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	plat, prof, err := h.svc.ResolvePlayer(r.Context(), r.PostFormValue("ref"), r.PostFormValue("platform"))
	switch {
	case errors.Is(err, service.ErrInvalidPlayerRef):
		render(w, r, http.StatusOK, views.FormError(sentence(err)))
		return
	case isNotFound(err):
		render(w, r, http.StatusOK, views.FormError("No "+plat.DisplayName+" player found for that ID."))
		return
	case err != nil:
		render(w, r, http.StatusOK, views.FormError(plat.DisplayName+" could not be reached: "+err.Error()))
		return
	}
	_, gerr := h.svc.PlayerByIdentity(r.Context(), plat.Name, prof.ExternalID)
	render(w, r, http.StatusOK, views.PlayerPreview(plat.Name, prof, gerr == nil))
}

func (h *Handler) addPlayer(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	p, err := h.svc.AddPlayer(r.Context(), r.PostFormValue("ref"), r.PostFormValue("platform"))
	switch {
	case errors.Is(err, service.ErrPlayerExists):
		h.toastOnly(w, r, toast.TypeWarning, "Already tracked", "This player is already archived here.")
		return
	case errors.Is(err, service.ErrInvalidPlayerRef), isNotFound(err):
		h.toastOnly(w, r, toast.TypeError, "Could not add player", sentence(err))
		return
	case err != nil:
		h.serverError(w, r, err)
		return
	}
	v, err := h.adminPlayersView(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.PlayerAdded(v, p.Name))
}

func (h *Handler) playerSummary(r *http.Request, id string) (service.PlayerSummary, error) {
	return h.svc.GetPlayerSummary(r.Context(), id)
}

func (h *Handler) setPlayerEnabled(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	id := r.PathValue("id")
	enabled := r.PostFormValue("enabled") == "true"
	if err := h.svc.SetPlayerEnabled(r.Context(), id, enabled); err != nil {
		if isNotFound(err) {
			h.toastOnly(w, r, toast.TypeError, "Player not found", "")
			return
		}
		h.serverError(w, r, err)
		return
	}
	title := "Tracking resumed"
	if !enabled {
		title = "Tracking paused"
	}
	h.renderRow(w, r, id, toast.TypeSuccess, title)
}

func (h *Handler) linkIdentity(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	id := r.PathValue("id")
	_, err := h.svc.LinkIdentity(r.Context(), id, r.PostFormValue("ref"), r.PostFormValue("platform"))
	switch {
	case errors.Is(err, service.ErrIdentityLinkedElsewhere):
		h.toastOnly(w, r, toast.TypeWarning, "Already tracked", sentence(err))
		return
	case errors.Is(err, service.ErrPlatformAlreadyLinked), errors.Is(err, service.ErrInvalidPlayerRef), isNotFound(err):
		h.toastOnly(w, r, toast.TypeError, "Could not link account", sentence(err))
		return
	case err != nil:
		h.serverError(w, r, err)
		return
	}
	h.renderRow(w, r, id, toast.TypeSuccess, "Account linked")
}

func (h *Handler) setIdentityEnabled(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	id := r.PathValue("id")
	enabled := r.PostFormValue("enabled") == "true"
	if err := h.svc.SetIdentityEnabled(r.Context(), id, r.PathValue("platform"), enabled); err != nil {
		if isNotFound(err) {
			h.toastOnly(w, r, toast.TypeError, "Account not found", "")
			return
		}
		h.serverError(w, r, err)
		return
	}
	title := "Account resumed"
	if !enabled {
		title = "Account paused"
	}
	h.renderRow(w, r, id, toast.TypeSuccess, title)
}

func (h *Handler) unlinkIdentity(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	id := r.PathValue("id")
	err := h.svc.UnlinkIdentity(r.Context(), id, r.PathValue("platform"), r.PostFormValue("delete_files") == "on")
	switch {
	case errors.Is(err, service.ErrLastIdentity):
		h.toastOnly(w, r, toast.TypeError, "Could not unlink account", sentence(err))
		return
	case isNotFound(err):
		h.toastOnly(w, r, toast.TypeError, "Account not found", "")
		return
	case err != nil:
		h.serverError(w, r, err)
		return
	}
	h.renderRow(w, r, id, toast.TypeSuccess, "Account unlinked")
}

func (h *Handler) mergePlayer(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	ctx := r.Context()
	src, serr := h.svc.GetPlayer(ctx, r.PathValue("id"))
	dst, derr := h.svc.GetPlayer(ctx, r.PostFormValue("into"))
	if serr != nil || derr != nil {
		h.toastOnly(w, r, toast.TypeError, "Could not merge", "Player not found.")
		return
	}
	err := h.svc.MergePlayers(ctx, src.ID, dst.ID)
	switch {
	case errors.Is(err, service.ErrMergeConflict), errors.Is(err, service.ErrMergeSelf), isNotFound(err):
		h.toastOnly(w, r, toast.TypeError, "Could not merge", sentence(err))
		return
	case err != nil:
		h.serverError(w, r, err)
		return
	}
	v, err := h.adminPlayersView(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.PlayersMerged(v, "Merged "+src.Name+" into "+dst.Name))
}

func (h *Handler) pollPlayer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.svc.RequestPoll(r.Context(), id); err != nil {
		if isNotFound(err) {
			h.toastOnly(w, r, toast.TypeError, "Player not found", "")
			return
		}
		h.serverError(w, r, err)
		return
	}
	h.toastOnly(w, r, toast.TypeInfo, "Poll queued", "The worker will poll this player next.")
}

func (h *Handler) deletePlayer(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	id := r.PathValue("id")
	p, err := h.svc.GetPlayer(r.Context(), id)
	if err != nil {
		if isNotFound(err) {
			h.toastOnly(w, r, toast.TypeError, "Player not found", "")
			return
		}
		h.serverError(w, r, err)
		return
	}
	if err := h.svc.DeletePlayer(r.Context(), id, r.PostFormValue("delete_files") == "on"); err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.ToastOOB(toast.TypeSuccess, "Stopped tracking "+p.Name, ""))
}

func (h *Handler) settingsPage(w http.ResponseWriter, r *http.Request) {
	st, err := h.svc.Settings(r.Context())
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.SettingsPage(h.page(r, "Settings"), views.SettingsView{Settings: st, Intervals: service.PollIntervals}))
}

func (h *Handler) saveSettings(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	d, _ := time.ParseDuration(r.PostFormValue("poll_interval"))
	st := service.Settings{InstanceTitle: r.PostFormValue("title"), PollInterval: d}
	v := views.SettingsView{Settings: st, Intervals: service.PollIntervals}
	if err := h.svc.UpdateSettings(r.Context(), st); err != nil {
		if !errors.Is(err, service.ErrInvalidSettings) {
			h.serverError(w, r, err)
			return
		}
		// "invalid settings: title must be 1-64 characters" → "Title must be 1-64 characters."
		v.Error = sentence(errors.New(strings.TrimPrefix(err.Error(), service.ErrInvalidSettings.Error()+": ")))
		render(w, r, http.StatusOK, views.GeneralSettings(v))
		return
	}
	render(w, r, http.StatusOK, views.GeneralSettingsSaved(v))
}

func (h *Handler) saveViewerSettings(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	st, err := h.svc.Settings(r.Context())
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	st.ViewerShowHeadset = r.PostFormValue("show_headset") == "on"
	st.ReplayViewer = r.PostFormValue("default_viewer")
	st.ViewerHeadsetColor = r.PostFormValue("headset_color")
	if a, err := strconv.ParseFloat(r.PostFormValue("headset_alpha"), 64); err == nil {
		st.ViewerHeadsetAlpha = a
	} else {
		st.ViewerHeadsetAlpha = -1
	}
	v := views.SettingsView{Settings: st, Intervals: service.PollIntervals}
	if err := h.svc.UpdateViewerSettings(r.Context(), st); err != nil {
		if !errors.Is(err, service.ErrInvalidSettings) {
			h.serverError(w, r, err)
			return
		}
		v.Error = sentence(errors.New(strings.TrimPrefix(err.Error(), service.ErrInvalidSettings.Error()+": ")))
		render(w, r, http.StatusOK, views.ViewerSettings(v))
		return
	}
	render(w, r, http.StatusOK, views.ViewerSettingsSaved(v))
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	u := httpx.UserFrom(r.Context())
	fail := func(msg string) { render(w, r, http.StatusOK, views.PasswordSettings(views.PasswordView{Error: msg})) }
	if r.PostFormValue("new") != r.PostFormValue("confirm") {
		fail("Passwords do not match.")
		return
	}
	err := h.svc.ChangePassword(r.Context(), u.ID, r.PostFormValue("current"), r.PostFormValue("new"))
	switch {
	case errors.Is(err, service.ErrInvalidCredentials):
		fail("Current password is incorrect.")
		return
	case errors.Is(err, service.ErrWeakPassword):
		fail(sentence(err))
		return
	case err != nil:
		h.serverError(w, r, err)
		return
	}
	h.clearSessionCookie(w, r)
	w.Header().Set("HX-Redirect", "/login")
	w.WriteHeader(http.StatusOK)
}
