package finding

import "testing"

func TestCVSS31ScoreMustMatchVector(t *testing.T) {
	input := Input{Title: "test", VulnerabilityType: "access_control", Location: "/item", Severity: "critical", AssetIDs: []string{"asset"}, CVSS: &CVSS{Version: "3.1", Vector: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", Score: 9.8}}
	if !validInput(input) {
		t.Fatal("valid CVSS 3.1 score rejected")
	}
	input.CVSS.Score = 8.8
	if validInput(input) {
		t.Fatal("mismatched CVSS score accepted")
	}
	input.CVSS.Score = 9.8
	input.CVSS.Vector = "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H/AV:P"
	if validInput(input) {
		t.Fatal("duplicate vector metric accepted")
	}
}
