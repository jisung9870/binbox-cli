package awsbrowser

import "strings"

// ELBV2RegionFromDNS recognizes the regional DNS form returned by the ELB
// APIs. It deliberately does not infer a resource from arbitrary AWS-looking
// hostnames; the ELBV2 provider still confirms the DNS name with
// DescribeLoadBalancers before emitting a canonical load balancer resource.
func ELBV2RegionFromDNS(partition, dnsName string) (string, bool) {
	dnsName = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(dnsName)), ".")
	dnsName = strings.TrimPrefix(dnsName, "dualstack.")

	suffix := ".elb.amazonaws.com"
	switch partition {
	case "aws", "aws-us-gov":
	case "aws-cn":
		suffix += ".cn"
	default:
		return "", false
	}
	if !strings.HasSuffix(dnsName, suffix) {
		return "", false
	}
	prefix := strings.TrimSuffix(dnsName, suffix)
	separator := strings.LastIndexByte(prefix, '.')
	if separator < 1 || separator == len(prefix)-1 {
		return "", false
	}
	region := prefix[separator+1:]
	if !regionNameRE.MatchString(region) || strings.TrimSpace(prefix[:separator]) == "" {
		return "", false
	}
	return region, true
}
