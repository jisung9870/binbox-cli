package integration

import "testing"

func TestExplicitContextValidationMatchesStrictCLIContract(t *testing.T) {
	for _, valid := range [][2]string{{"", ""}, {"dev", ""}, {"dev-1", "ap-northeast-2"}, {"", "us-east-1"}} {
		if !validExplicitContextRequest(valid[0], valid[1]) {
			t.Fatalf("valid context rejected: profile=%q region=%q", valid[0], valid[1])
		}
	}
	for _, invalid := range [][2]string{{".hidden", ""}, {"-hidden", ""}, {" dev", ""}, {"", "not-a-region"}, {"", "us-east-1 "}} {
		if validExplicitContextRequest(invalid[0], invalid[1]) {
			t.Fatalf("invalid context accepted: profile=%q region=%q", invalid[0], invalid[1])
		}
	}
}
