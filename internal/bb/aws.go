package bb

import (
	"fmt"
	"sort"
	"strings"
)

func (a *App) aws(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(a.out, `Usage:
  bb aws browse [--profile NAME] [--region REGION]
                                  Browse read-only AWS resources and links
  bb aws query ec2 instances [--profile NAME] [--region REGION] [--json]
  bb aws query ami <ami-id> [--profile NAME] [--region REGION] [--scope current|all] [--json]
  bb aws query domain <fqdn> [--profile NAME] [--region REGION] [--scope current|all] [--json]
  bb aws query role <exact-name> [--profile NAME] [--region REGION] [--scope current|all] [--json]
                                  Run one scoped read-only AWS query
  bb aws sync <sg|graph> --group NAME [--json]
                                  Collect an explicit SG-only or combined AWS graph snapshot
  bb aws refs <sg|vpc> <resource-id> --account ID --region REGION [--partition PARTITION] [--json]
                                  Find observed incoming references in the active snapshot
  bb aws sso [session]             Select or log in to an AWS SSO session
  bb aws sso list                  List configured AWS SSO sessions
  bb aws assume [profile]          Select or apply AWS CLI-resolved credentials
  bb aws assume list|current|unset
  bb aws assume exec <profile> -- <command> [args...]

SSO login is session-scoped. Assume is profile-scoped because a profile selects
an AWS account, role, and region. The AWS CLI owns tokens and credentials.
`)
		return err
	}
	switch args[0] {
	case "browse":
		return a.awsBrowse(args[1:])
	case "query":
		return a.awsQuery(args[1:])
	case "sync":
		return a.awsSnapshotSyncCommand(args[1:])
	case "refs":
		return a.awsSnapshotRefsCommand(args[1:])
	case "sso":
		return a.awsSSO(args[1:])
	case "assume":
		return a.assume(args[1:])
	default:
		return invalid(fmt.Sprintf("unknown aws command %q", args[0]))
	}
}

func ssoSessionNames(data []byte) []string {
	var names []string
	for _, section := range iniSections(strings.Split(string(data), "\n")) {
		if strings.HasPrefix(section.name, "sso-session ") {
			names = append(names, strings.TrimPrefix(section.name, "sso-session "))
		}
	}
	sort.Strings(names)
	return names
}

func (a *App) chooseSSOSession() (string, error) {
	config, err := a.readAWSConfig()
	if err != nil {
		return "", err
	}
	names := ssoSessionNames(config)
	choices := make([]selectChoice, len(names))
	for i, name := range names {
		fields := sectionFields(config, "sso-session "+name)
		choices[i] = selectChoice{
			Value:       name,
			Label:       name,
			Description: fields["sso_region"],
			SearchText:  fields["sso_start_url"],
		}
	}
	return a.selectOne("AWS SSO session", choices)
}

func (a *App) awsSSO(args []string) error {
	if helpRequested(args) {
		_, err := fmt.Fprint(a.out, `Usage:
  bb aws sso [session]
  bb aws sso list

Without a session, select one from configured [sso-session NAME] sections.
`)
		return err
	}
	if len(args) == 1 && args[0] == "list" {
		config, err := a.readAWSConfig()
		if err != nil {
			return err
		}
		for _, name := range ssoSessionNames(config) {
			fmt.Fprintln(a.out, name)
		}
		return nil
	}
	if len(args) > 1 {
		return usage("aws sso", "[session]")
	}
	session := ""
	var err error
	if len(args) == 1 {
		session = args[0]
		if !awsProfileNameRE.MatchString(session) {
			return invalid("invalid AWS SSO session name")
		}
	} else {
		session, err = a.chooseSSOSession()
		if err != nil || session == "" {
			return err
		}
	}
	config, err := a.readAWSConfig()
	if err != nil {
		return err
	}
	found := false
	for _, name := range ssoSessionNames(config) {
		if name == session {
			found = true
			break
		}
	}
	if !found {
		return invalid("AWS SSO session not found: " + session)
	}
	return a.runExternal("aws", "sso", "login", "--sso-session", session)
}
