package preset

import "testing"

func TestDerivedOutputs(t *testing.T) {
	if o := DerivedOutputs("poster", nil); len(o) != 2 || o[0].Format != "avif" || o[1].Format != "webp" || o[0].Name != "poster" {
		t.Fatalf("default: %+v", o)
	}
	if o := DerivedOutputs("thumb", []string{"avif"}); len(o) != 1 || o[0].Format != "avif" || o[0].Name != "thumb" {
		t.Fatalf("avif only: %+v", o)
	}
	for _, ok := range [][]string{nil, {"avif"}, {"webp"}, {"webp", "avif"}} {
		if !ValidDerivedFormats(ok) {
			t.Errorf("%v rejected", ok)
		}
	}
	for _, bad := range [][]string{{"avif", "avif"}, {"jpeg"}, {""}} {
		if ValidDerivedFormats(bad) {
			t.Errorf("%v accepted", bad)
		}
	}
}
