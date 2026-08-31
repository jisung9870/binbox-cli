package awsbrowser

import "testing"

func TestELBV2RegionFromDNSRecognizesSupportedLayoutsAndPartitions(t *testing.T) {
	for _, test := range []struct {
		partition string
		dns       string
		region    string
	}{
		{"aws", "dualstack.api-123.elb.ap-northeast-2.amazonaws.com.", "ap-northeast-2"},
		{"aws", "m-nlb-udg-kr-pmm-24c45c678d7f8951.elb.ap-northeast-2.amazonaws.com.", "ap-northeast-2"},
		{"aws", "dualstack.r-alb-udg-kr-game-722267338.ap-northeast-2.elb.amazonaws.com.", "ap-northeast-2"},
		{"aws", "internal-api-123.us-east-1.elb.amazonaws.com", "us-east-1"},
		{"aws-us-gov", "internal-api-123.elb.us-gov-west-1.amazonaws.com", "us-gov-west-1"},
		{"aws-us-gov", "internal-api-123.us-gov-west-1.elb.amazonaws.com", "us-gov-west-1"},
		{"aws-cn", "api-123.elb.cn-north-1.amazonaws.com.cn", "cn-north-1"},
		{"aws-cn", "api-123.cn-north-1.elb.amazonaws.com.cn", "cn-north-1"},
	} {
		region, ok := ELBV2RegionFromDNS(test.partition, test.dns)
		if !ok || region != test.region {
			t.Fatalf("partition=%q dns=%q region=%q ok=%v", test.partition, test.dns, region, ok)
		}
	}
	for _, test := range []struct{ partition, dns string }{
		{"aws", "api.example.com"},
		{"aws", "elb.us-east-1.amazonaws.com"},
		{"aws", "api-123.invalid.elb.amazonaws.com"},
		{"aws", "api-123.elb.cn-north-1.amazonaws.com.cn"},
		{"aws-iso", "api-123.us-iso-east-1.elb.c2s.ic.gov"},
	} {
		if region, ok := ELBV2RegionFromDNS(test.partition, test.dns); ok {
			t.Fatalf("unsupported DNS accepted: partition=%q dns=%q region=%q", test.partition, test.dns, region)
		}
	}
}
