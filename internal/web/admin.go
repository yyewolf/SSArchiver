package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/yyewolf/ssarchiver/internal/httpx"
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
	return views.AdminPlayersView{Players: players, Now: h.svc.Now()}, err
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
	pl, err := h.playerSummary(r, id)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	title := "Tracking resumed"
	if !enabled {
		title = "Tracking paused"
	}
	render(w, r, http.StatusOK, views.PlayerRowToast(pl, h.svc.Now(), title))
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
