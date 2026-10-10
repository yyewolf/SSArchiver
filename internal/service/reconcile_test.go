package service_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/storage"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestReconcileStorage(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	a := mustAdd(t, svc, "1001")
	upsert(t, svc, a, clk, []scoreItem{{1, true}, {2, true}, {3, true}})
	st := svc.Store()
	dat := func(n int64) storage.Loc { return storage.Loc{PlayerID: a, RowID: n, Ext: ".dat"} }
	if _, _, err := st.Put(dat(1), strings.NewReader("adopt me")); err != nil { // file, row pending
		t.Fatal(err)
	}
	if err := svc.MarkReplayArchived(ctx, 2, 5, "x"); err != nil { // row archived, no file
		t.Fatal(err)
	}
	if _, _, err := st.Put(dat(999), strings.NewReader("orphan")); err != nil { // file, no row
		t.Fatal(err)
	}
	if err := os.WriteFile(st.Path(dat(3))+".tmp", []byte("partial"), 0o640); err != nil {
		t.Fatal(err)
	}
	res, err := svc.ReconcileStorage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := service.ReconcileResult{RemovedTmp: 1, Adopted: 1, Orphans: 1, Requeued: 1}
	if res != want || !res.Changed() {
		t.Fatalf("result = %+v, want %+v", res, want)
	}
	s1, _ := svc.GetScore(ctx, 1)
	s2, _ := svc.GetScore(ctx, 2)
	if s1.ReplayState != model.ReplayArchived || s1.ReplaySize != int64(len("adopt me")) {
		t.Fatalf("score 1 not adopted: %+v", s1)
	}
	if s2.ReplayState != model.ReplayPending || s2.ReplaySHA256 != "" || s2.ArchivedAt != nil {
		t.Fatalf("score 2 not requeued: %+v", s2)
	}
	if _, err := os.Stat(st.Path(dat(999))); err != nil {
		t.Fatal("orphan files must be kept")
	}
	again, _ := svc.ReconcileStorage(ctx)
	if again.Changed() && again != (service.ReconcileResult{Orphans: 1}) {
		t.Fatalf("second run should only report the orphan: %+v", again)
	}
}
