package bb

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// browsePlanFixture exercises every rendering rule at once: a sensitive value on
// both sides, a value that is only known after apply, a plain scalar change, all
// four action shapes, and an unchanged resource that must not be listed.
const browsePlanFixture = `{"resource_changes":[
 {"address":"aws_db_instance.main","change":{
   "actions":["update"],
   "before":{"allocated_storage":10,"endpoint":"old.example.com","password":"super-secret-value"},
   "after":{"allocated_storage":20,"endpoint":null,"password":"rotated-secret-value"},
   "before_sensitive":{"password":true},
   "after_sensitive":{"password":true},
   "after_unknown":{"endpoint":true}}},
 {"address":"aws_s3_bucket.logs","change":{
   "actions":["delete"],
   "before":{"bucket":"logs-bucket"},"after":null}},
 {"address":"aws_instance.web","change":{
   "actions":["create"],
   "before":null,"after":{"ami":"ami-123"}}},
 {"address":"aws_lb.edge","change":{
   "actions":["delete","create"],
   "before":{"name":"edge-old"},"after":{"name":"edge-new"}}},
 {"address":"aws_vpc.main","change":{
   "actions":["no-op"],
   "before":{"cidr":"10.0.0.0/16"},"after":{"cidr":"10.0.0.0/16"}}}
]}`

const browsePlanSecret = "super-secret-value"

func browseApp(t *testing.T) (*App, *strings.Builder) {
	t.Helper()
	a, _, _ := tfxTestApp(t)
	stdout := new(strings.Builder)
	a.out = stdout

	dir := t.TempDir()
	fixture := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(fixture, []byte(browsePlanFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(dir, "tfplan")
	if err := os.WriteFile(plan, []byte("plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.env = append(a.env, "GO_WANT_BB_TFX_SHOW_FILE="+fixture, "TFPLAN_FILE="+plan)
	return a, stdout
}

func TestTFXBrowseRedactsSensitiveAndUnknownValues(t *testing.T) {
	summaries, err := summarizeTFXPlan([]byte(browsePlanFixture), defaultTFXReviewRules())
	if err != nil {
		t.Fatal(err)
	}
	var database tfxResourceSummary
	for _, resource := range summaries {
		if resource.Address == "aws_db_instance.main" {
			database = resource
		}
	}
	got := map[string][2]string{}
	for _, attribute := range database.Attributes {
		got[attribute.Path] = [2]string{attribute.Before, attribute.After}
	}
	want := map[string][2]string{
		"allocated_storage": {"10", "20"},
		"endpoint":          {"old.example.com", tfxUnknownValue},
		"password":          {tfxSensitiveValue, tfxSensitiveValue},
	}
	for path, values := range want {
		if got[path] != values {
			t.Fatalf("attribute %s = %v, want %v", path, got[path], values)
		}
	}
	if len(database.Attributes) != len(want) {
		t.Fatalf("attributes=%+v", database.Attributes)
	}

	encoded, err := json.Marshal(summaries)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), browsePlanSecret) || strings.Contains(string(encoded), "rotated-secret-value") {
		t.Fatalf("summary carries a sensitive value:\n%s", encoded)
	}
}

func TestTFXBrowseOrdersByBlastRadiusAndSkipsUnchanged(t *testing.T) {
	summaries, err := summarizeTFXPlan([]byte(browsePlanFixture), defaultTFXReviewRules())
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, resource := range summaries {
		order = append(order, resource.Action+" "+resource.Address)
	}
	want := []string{
		"destroy aws_s3_bucket.logs",
		"replace aws_lb.edge",
		"update aws_db_instance.main",
		"create aws_instance.web",
	}
	if !slicesEqual(order, want) {
		t.Fatalf("order=%v\nwant=%v", order, want)
	}
}

func TestTFXBrowseMarksReviewUsingTheSameRulesAsClassify(t *testing.T) {
	rules := defaultTFXReviewRules()
	tagOnly := `{"resource_changes":[{"address":"aws_instance.web","change":{
		"actions":["update"],"before":{"tags":{"env":"dev"}},"after":{"tags":{"env":"prod"}}}}]}`

	summaries, err := summarizeTFXPlan([]byte(tagOnly), rules)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Review {
		t.Fatalf("tag-only update should not need review: %+v", summaries)
	}
	verdict, _, err := classifyTFXPlan([]byte(tagOnly), rules)
	if err != nil || verdict != "EXPECTED" {
		t.Fatalf("classify verdict=%q err=%v", verdict, err)
	}

	summaries, err = summarizeTFXPlan([]byte(browsePlanFixture), rules)
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range summaries {
		if !resource.Review {
			t.Fatalf("%s should need review under the default rules", resource.Address)
		}
	}
	if verdict, _, err = classifyTFXPlan([]byte(browsePlanFixture), rules); err != nil || verdict != "REVIEW" {
		t.Fatalf("classify verdict=%q err=%v", verdict, err)
	}
}

func TestTFXBrowseJSONEnvelopeCountsAndHidesSecrets(t *testing.T) {
	a, stdout := browseApp(t)
	if err := a.Run([]string{"tfx", "browse", "--json"}); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		SchemaVersion int  `json:"schema_version"`
		OK            bool `json:"ok"`
		Data          struct {
			Changes     int                  `json:"changes"`
			Destroy     int                  `json:"destroy"`
			NeedsReview int                  `json:"needs_review"`
			Resources   []tfxResourceSummary `json:"resources"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &decoded); err != nil {
		t.Fatalf("envelope=%q err=%v", stdout.String(), err)
	}
	if decoded.SchemaVersion != SchemaVersion || !decoded.OK {
		t.Fatalf("envelope=%+v", decoded)
	}
	if decoded.Data.Changes != 4 || decoded.Data.Destroy != 2 || decoded.Data.NeedsReview != 4 {
		t.Fatalf("counts=%+v", decoded.Data)
	}
	if strings.Contains(stdout.String(), browsePlanSecret) {
		t.Fatalf("JSON output leaked a sensitive value:\n%s", stdout.String())
	}
}

func TestTFXBrowseWithoutATerminalPrintsATableInsteadOfPrompting(t *testing.T) {
	a, stdout := browseApp(t)
	// No terminal and no input: a prompt here would block or consume an answer
	// the command has no use for.
	a.in = strings.NewReader("")
	if err := a.Run([]string{"tfx", "browse"}); err != nil {
		t.Fatal(err)
	}
	rendered := stdout.String()
	for _, want := range []string{"aws_s3_bucket.logs", "destroy", "replace", "Changed Attributes"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("table missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, browsePlanSecret) {
		t.Fatalf("table leaked a sensitive value:\n%s", rendered)
	}
	if strings.Contains(rendered, "[1-") {
		t.Fatalf("table output prompted for a selection:\n%s", rendered)
	}
}

func TestTFXBrowseReportsAnEmptyPlanAndAMissingPlanFile(t *testing.T) {
	a, stdout := browseApp(t)
	empty := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(empty, []byte(`{"resource_changes":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a.env = append(a.env, "GO_WANT_BB_TFX_SHOW_FILE="+empty)
	if err := a.Run([]string{"tfx", "browse"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Plan has no changes.") {
		t.Fatalf("empty plan output=%q", stdout.String())
	}

	// A named plan overrides TFPLAN_FILE, and an absent one is reported rather
	// than handed to terraform.
	missing, _ := browseApp(t)
	err := missing.Run([]string{"tfx", "browse", filepath.Join(t.TempDir(), "absent")})
	if err == nil || !strings.Contains(err.Error(), "plan file not found") {
		t.Fatalf("missing plan err=%v", err)
	}
}

func TestTFXBrowseOnlyReadsThePlan(t *testing.T) {
	a, _ := browseApp(t)
	var invoked [][]string
	inner := a.command
	a.command = func(name string, args ...string) *exec.Cmd {
		invoked = append(invoked, append([]string{name}, args...))
		return inner(name, args...)
	}
	if err := a.Run([]string{"tfx", "browse", "--json"}); err != nil {
		t.Fatal(err)
	}
	if len(invoked) != 1 {
		t.Fatalf("browse ran %d commands: %v", len(invoked), invoked)
	}
	if got := strings.Join(invoked[0][:3], " "); got != "terraform show -json" {
		t.Fatalf("browse ran %v", invoked[0])
	}
}
