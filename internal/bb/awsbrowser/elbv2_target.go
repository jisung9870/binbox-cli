package awsbrowser

import "strings"

// ELBV2RegionFromDNS recognizes the name-id.elb.region partition suffix used
// by Application and Network Load Balancers. It deliberately excludes the
// Classic Load Balancer name-id.region.elb suffix; the ELBV2 provider still
// confirms an accepted DNS name with DescribeLoadBalancers.
func ELBV2RegionFromDNS(partition, dnsName string) (string, bool) {
	dnsName = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(dnsName)), ".")
	dnsName = strings.TrimPrefix(dnsName, "dualstack.")

	suffix := ".amazonaws.com"
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
	separator := strings.LastIndex(prefix, ".elb.")
	if separator < 1 || separator+len(".elb.") == len(prefix) {
		return "", false
	}
	region := prefix[separator+len(".elb."):]
	if !regionNameRE.MatchString(region) || strings.TrimSpace(prefix[:separator]) == "" {
		return "", false
	}
	return region, true
}
