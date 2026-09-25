package engine_test

import "testing"

// A deliberately green "red": this test passes without any change to the
// engine, so redcheck must refuse the commit that adds it (#13, E0's last
// item). The pull request carrying it is closed unmerged.
func TestRedcheckBlocksATestThatPassesWithoutItsChange(t *testing.T) {
	if 1+1 != 2 {
		t.Fatal("arithmetic broke")
	}
}
