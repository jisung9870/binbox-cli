package awsbrowser

import "strings"

// ELBV2RegionFromDNS recognizes both AWS ELB DNS layouts used by Application
// and Network Load Balancers. This is only a regional lookup hint; the ELBV2
// provider confirms the DNS name with DescribeLoadBalancers before promoting
// it to a canonical resource.
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
	resourceName, region := "", ""
	if separator := strings.LastIndex(prefix, ".elb."); separator >= 1 && separator+len(".elb.") < len(prefix) {
		resourceName = prefix[:separator]
		region = prefix[separator+len(".elb."):]
	} else if strings.HasSuffix(prefix, ".elb") {
		withoutELB := strings.TrimSuffix(prefix, ".elb")
		if separator := strings.LastIndex(withoutELB, "."); separator >= 1 && separator+1 < len(withoutELB) {
			resourceName = withoutELB[:separator]
			region = withoutELB[separator+1:]
		}
	}
	if !regionNameRE.MatchString(region) || strings.TrimSpace(resourceName) == "" {
		return "", false
	}
	return region, true
}
