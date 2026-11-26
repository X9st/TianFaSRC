package skill

import (
	"archive/zip"
	"bytes"
	"testing"
)

func makeZIP(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestValidateZIPRequiresSafeSkillPackage(t *testing.T) {
	valid := "---\nname: web-recon\nversion: 1.0.0\ndescription: 授权 Web 侦察\ncompatibility: '>=1.0.0'\nagents: [worker]\nmcps: [browser]\ncapabilities: [network.http]\n---\n仅使用授权范围内工具。"
	manifest, body, hash, err := ValidateZIP(makeZIP(t, map[string]string{"SKILL.md": valid, "references/guide.md": "guide"}))
	if err != nil || manifest.Name != "web-recon" || len(manifest.MCPs) != 1 || body != valid || len(hash) != 64 {
		t.Fatalf("manifest=%+v hash=%s err=%v", manifest, hash, err)
	}
	for _, files := range []map[string]string{
		{"../escape": "bad", "SKILL.md": valid},
		{"/absolute": "bad", "SKILL.md": valid},
		{"SKILL.md": "---\nname: invalid\nversion: 1\ndescription: x\nagents: [unknown]\n---\nbody"},
	} {
		if _, _, _, err := ValidateZIP(makeZIP(t, files)); err == nil {
			t.Fatalf("accepted unsafe skill package: %+v", files)
		}
	}
}
