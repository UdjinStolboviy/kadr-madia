package main

import (
	"context"
	"testing"

	"github.com/UdjinStolboviy/kadr-madia/internal/db/dbtest"
)

func TestInspectDB(t *testing.T) {
	ctx := context.Background()
	store := dbtest.Open(t)
	r, err := inspectDB(ctx, store.DB().Path)
	if err != nil || r.integrity != "ok" || r.migrations == 0 {
		t.Fatalf("%+v %v", r, err)
	}
	for _, tb := range coreTables {
		if _, ok := r.counts[tb]; !ok {
			t.Errorf("table %s not counted", tb)
		}
	}
}
