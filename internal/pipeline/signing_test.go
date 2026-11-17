package pipeline

import "testing"

// Vectors shared with sdk/node/test/signing.test.ts and edge/kadr-edge.
func TestSignKeyMatchesNodeSDK(t *testing.T) {
	const secret, key = "test-secret-0123456789abcdef", "acme/blb_01JDTEST/main.avif"
	if got := SignKey(secret, key, 1763330400, "receipt.avif"); got != "6htVHR_ussyeRR5QQBVvsmobaFla_5qrDeHoDtYVw5M" {
		t.Fatalf("with download: %s", got)
	}
	if got := SignKey(secret, key, 1763330400, ""); got != "O15QXzAjaI3KPNfA_FguxK3zQPZD0s-YMgTTH8SDHNU" {
		t.Fatalf("without download: %s", got)
	}
}

// Vector shared with edge/kadr-edge/test/worker.test.ts (prefix=1 mode).
func TestSignKeyPrefixVector(t *testing.T) {
	const secret = "test-secret-0123456789abcdef"
	for _, key := range []string{"acme/blb_01/hls/master.m3u8", "acme/blb_01/hls/seg_360p_00000.m4s"} {
		if got := SignKeyPrefix(secret, key, 1763330400); got != "eEC8YEe8hi8C4T_dfxNBkoR8szjp_oOArAdYjWghpa0" {
			t.Fatalf("%s: %s", key, got)
		}
	}
}
