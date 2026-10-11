package web

import (
	"errors"
	"io"
	"net/http"

	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/components/toast"
	"github.com/yyewolf/ssarchiver/internal/web/views"
)

// errFileTooLarge is the skip reason for a part over the per-file cap: the
// service surfaces storage reader errors through the result's Reason.
var errFileTooLarge = errors.New("file too large")

// limitReader caps one uploaded file while feeding the request-wide byte
// count: reads past the cap fail with errFileTooLarge, and a file that ends
// exactly at the cap is accepted.
type limitReader struct {
	r     io.Reader
	left  int64  // bytes still allowed before the cap trips
	total *int64 // bytes read for the whole request so far
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		// The cap is reached: one more byte tells EOF from overflow.
		var probe [1]byte
		n, err := l.r.Read(probe[:])
		l.add(n)
		if n > 0 {
			return 0, errFileTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.add(n)
	l.left -= int64(n)
	return n, err
}

func (l *limitReader) add(n int) {
	if n > 0 {
		*l.total += int64(n)
	}
}

// importReplays streams uploaded .bsor files through ImportReplay one part at
// a time (spec §6): every file gets a result, later parts are skipped once
// the request-wide cap is crossed, and the response is the refreshed players
// table plus the out-of-band results panel and a summary toast.
func (h *Handler) importReplays(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		h.toastOnly(w, r, toast.TypeError, "No files uploaded", "")
		return
	}
	results := []service.ImportResult{}
	var total int64
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			h.serverError(w, r, err)
			return
		}
		if part.FormName() != "files" {
			continue
		}
		if part.FileName() == "" { // a value part named "files" is not an upload
			continue
		}
		if total >= h.importTotalMax { // the total is only known after streaming a part
			results = append(results, service.ImportResult{
				File: part.FileName(), Status: service.ImportStatusSkipped, Reason: "request size limit reached",
			})
			continue
		}
		lr := &limitReader{r: part, left: h.importFileMax, total: &total}
		res, err := h.svc.ImportReplay(r.Context(), part.FileName(), lr)
		if err != nil { // fatal only (cancelled context)
			h.serverError(w, r, err)
			return
		}
		results = append(results, res)
	}
	if len(results) == 0 {
		h.toastOnly(w, r, toast.TypeError, "No files uploaded", "")
		return
	}
	v, err := h.adminPlayersView(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.ImportDone(v, results))
}
