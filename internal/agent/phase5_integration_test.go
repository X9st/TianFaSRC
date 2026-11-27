package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/X9st/TianFaSRC/internal/backup"
	"github.com/X9st/TianFaSRC/internal/blob"
	"github.com/X9st/TianFaSRC/internal/finding"
	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/policy"
	"github.com/X9st/TianFaSRC/internal/recordproxy"
	"github.com/X9st/TianFaSRC/internal/report"
	"github.com/X9st/TianFaSRC/internal/secret"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/X9st/TianFaSRC/internal/tool"
	"github.com/X9st/TianFaSRC/internal/traffic"
)

func TestWorkerFindingReportRequiresRealObservation(t *testing.T) {
	scheduler, orgID, taskID, intentID := schedulerFixture(t)
	ctx := context.Background()
	var actorID string
	if err := scheduler.Engine.Pool.QueryRow(ctx, `SELECT created_by FROM tasks WHERE id=$1`, taskID).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("observed")) }))
	defer target.Close()
	parsed, _ := url.Parse(target.URL)
	port, _ := strconv.Atoi(parsed.Port())
	if _, err := scheduler.Engine.Pool.Exec(ctx, `UPDATE task_scopes SET kind='ip',value='127.0.0.1',normalized_value='127.0.0.1',ports=ARRAY[$2]::int[] WHERE task_id=$1`, taskID, port); err != nil {
		t.Fatal(err)
	}
	asset, err := (task.Store{Pool: scheduler.Engine.Pool}).UpsertAsset(ctx, orgID, actorID, task.AssetInput{Kind: "endpoint", Value: target.URL + "/", SourceTaskID: &taskID})
	if err != nil {
		t.Fatal(err)
	}
	claim, ok, err := scheduler.ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if _, err := scheduler.Engine.Pool.Exec(ctx, `UPDATE task_intents SET status='running' WHERE id=$1`, intentID); err != nil {
		t.Fatal(err)
	}
	d := tool.Dispatcher{Pool: scheduler.Engine.Pool, DataDir: t.TempDir(), Enforcer: policy.Enforcer{TestOnlyAllowPrivate: true}}
	payload, _ := json.Marshal(map[string]any{"title": "对象级授权缺失", "vulnerability_type": "bola", "location": "/", "severity": "high", "asset_ids": []string{asset.ID}, "impact": "可读取其他数据", "reproduction_steps": []string{"GET /"}, "observed_result": "返回私有数据", "recommendation": "校验对象归属"})
	call := tool.Call{OrganizationID: orgID, TaskID: taskID, IntentID: intentID, RunID: claim.RunID, ActorID: actorID, Name: "finding.report", Input: payload, IdempotencyKey: "phase5-finding-before-observation"}
	blocked, err := d.Execute(ctx, call)
	if !errors.Is(err, tool.ErrToolBlocked) || blocked.Status != "blocked" {
		t.Fatalf("unobserved finding accepted: %+v %v", blocked, err)
	}
	httpInput, _ := json.Marshal(map[string]string{"url": target.URL, "method": "GET"})
	call.Name = "http.read"
	call.Input = httpInput
	call.IdempotencyKey = "phase5-http-before-finding"
	observed, err := d.Execute(ctx, call)
	if err != nil || observed.Status != "succeeded" {
		t.Fatalf("HTTP observe: %+v %v", observed, err)
	}
	call.Name = "finding.report"
	call.Input = payload
	call.IdempotencyKey = "phase5-finding-after-observation"
	reported, err := d.Execute(ctx, call)
	if err != nil || reported.Status != "succeeded" {
		t.Fatalf("finding report: %+v %v", reported, err)
	}
	items, err := (finding.Store{Pool: scheduler.Engine.Pool}).List(ctx, orgID, taskID)
	if err != nil || len(items) != 1 || items[0].Status != "draft" || items[0].SourceIntentID == nil || *items[0].SourceIntentID != intentID {
		t.Fatalf("reported findings: %+v %v", items, err)
	}
}

func TestRecordingProxyHTTPAndHTTPSRespectScope(t *testing.T) {
	scheduler, orgID, taskID, _ := schedulerFixture(t)
	ctx := context.Background()
	var actorID string
	if err := scheduler.Engine.Pool.QueryRow(ctx, `SELECT created_by FROM tasks WHERE id=$1`, taskID).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	httpTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("http-observed")) }))
	defer httpTarget.Close()
	httpsTarget := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("https-observed")) }))
	defer httpsTarget.Close()
	portOf := func(raw string) int {
		parsed, _ := url.Parse(raw)
		value, _ := strconv.Atoi(parsed.Port())
		return value
	}
	httpPort, httpsPort := portOf(httpTarget.URL), portOf(httpsTarget.URL)
	if _, err := scheduler.Engine.Pool.Exec(ctx, `UPDATE task_scopes SET kind='ip',value='127.0.0.1',normalized_value='127.0.0.1',ports=ARRAY[$2,$3]::int[] WHERE task_id=$1`, taskID, httpPort, httpsPort); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ca, err := recordproxy.OpenCA(root)
	if err != nil {
		t.Fatal(err)
	}
	upstreamRoots := x509.NewCertPool()
	upstreamRoots.AddCert(httpsTarget.Certificate())
	proxyHandler := recordproxy.Proxy{CA: ca, Tokens: recordproxy.TokenStore{Pool: scheduler.Engine.Pool}, Tasks: task.Store{Pool: scheduler.Engine.Pool}, Traffic: traffic.Store{Pool: scheduler.Engine.Pool, Blobs: blob.Store{Root: root}}, Enforcer: policy.Enforcer{TestOnlyAllowPrivate: true}, TestOnlyTLSRootCAs: upstreamRoots}
	proxyServer := httptest.NewServer(proxyHandler)
	defer proxyServer.Close()
	token, err := (recordproxy.TokenStore{Pool: scheduler.Engine.Pool}).Issue(ctx, orgID, actorID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, _ := url.Parse(proxyServer.URL)
	proxyURL.User = url.UserPassword("tianfa", token.Value)
	clientRoots := x509.NewCertPool()
	if !clientRoots.AppendCertsFromPEM(ca.Certificate) {
		t.Fatal("CA unavailable")
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: clientRoots}}}
	for _, test := range []struct{ url, want string }{{httpTarget.URL, "http-observed"}, {httpsTarget.URL, "https-observed"}} {
		response, err := client.Get(test.url)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 || string(body) != test.want {
			t.Fatalf("proxy %s: %d %q", test.url, response.StatusCode, body)
		}
	}
	blockedURL := strings.Replace(httpTarget.URL, "127.0.0.1", "127.0.0.2", 1)
	response, err := client.Get(blockedURL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatalf("out-of-scope proxy status=%d", response.StatusCode)
	}
	items, err := (traffic.Store{Pool: scheduler.Engine.Pool, Blobs: blob.Store{Root: root}}).List(ctx, orgID, traffic.Filter{TaskID: taskID})
	if err != nil || len(items) != 2 {
		t.Fatalf("captured traffic=%d err=%v", len(items), err)
	}
}

func TestEvidenceSurvivesTrafficDeletionAndRejectsTampering(t *testing.T) {
	scheduler, orgID, taskID, intentID := schedulerFixture(t)
	ctx := context.Background()
	var actorID string
	if err := scheduler.Engine.Pool.QueryRow(ctx, `SELECT created_by FROM tasks WHERE id=$1`, taskID).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	asset, err := (task.Store{Pool: scheduler.Engine.Pool}).UpsertAsset(ctx, orgID, actorID, task.AssetInput{Kind: "endpoint", Value: "https://example.invalid/private", SourceTaskID: &taskID})
	if err != nil {
		t.Fatal(err)
	}
	blobs := blob.Store{Root: t.TempDir()}
	trafficStore := traffic.Store{Pool: scheduler.Engine.Pool, Blobs: blobs}
	findingStore := finding.Store{Pool: scheduler.Engine.Pool, Blobs: blobs}
	now := time.Now()
	request := []byte("GET /private HTTP/1.1\r\nHost: example.invalid\r\nAuthorization: test-secret\r\n\r\n")
	response := []byte("HTTP/1.1 200 OK\r\nContent-Length: 8\r\n\r\nprivate!")
	exchange, err := trafficStore.Record(ctx, traffic.RecordInput{OrganizationID: orgID, TaskID: taskID, IntentID: intentID, Method: "GET", URL: "https://example.invalid/private", RemoteIP: "203.0.113.10", StatusCode: 200, RequestHeaders: map[string][]string{"Authorization": {"test-secret"}}, ResponseHeaders: map[string][]string{"Content-Type": {"text/plain"}}, RequestRaw: request, ResponseRaw: response, RequestedAt: now, RespondedAt: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	masked, err := trafficStore.Get(ctx, orgID, exchange.ID, false)
	if err != nil || masked.RequestHeaders["Authorization"][0] == "test-secret" {
		t.Fatalf("traffic header mask: %+v %v", masked, err)
	}
	if masked.RequestBodyBytes != 0 || masked.ResponseBodyBytes != 8 || len(masked.ResponseBodySHA256) != 64 {
		t.Fatalf("body metadata: %+v", masked)
	}
	f, err := findingStore.Create(ctx, orgID, actorID, finding.Input{TaskID: taskID, Title: "对象越权读取", VulnerabilityType: "broken_object_level_authorization", Location: "/private", Severity: "high", AssetIDs: []string{asset.ID}, SourceIntentID: intentID, Impact: "可读取其他对象", ReproductionSteps: []string{"使用低权限账户请求 /private"}, ObservedResult: "返回私有内容", Recommendation: "校验对象归属"})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := findingStore.Create(ctx, orgID, actorID, finding.Input{TaskID: taskID, Title: "重复报告", VulnerabilityType: "broken_object_level_authorization", Location: "/private", Severity: "high", AssetIDs: []string{asset.ID}})
	if err != nil || duplicate.ID != f.ID {
		t.Fatalf("finding dedup: %+v %v", duplicate, err)
	}
	evidence, err := findingStore.BindTraffic(ctx, orgID, actorID, f.ID, exchange.ID, "proof", "低权限请求返回私有内容")
	if err != nil {
		t.Fatal(err)
	}
	again, err := findingStore.BindTraffic(ctx, orgID, actorID, f.ID, exchange.ID, "proof", "重复绑定")
	if err != nil || again.BindingID != evidence.BindingID {
		t.Fatalf("binding idempotency: %+v %v", again, err)
	}
	if err := trafficStore.Delete(ctx, orgID, exchange.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := trafficStore.Get(ctx, orgID, exchange.ID, false); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("traffic still visible: %v", err)
	}
	actual, err := findingStore.ReadEvidence(ctx, orgID, evidence.BindingID, "response")
	if err != nil || !bytes.Equal(actual, response) {
		t.Fatalf("evidence lost after traffic deletion: %q %v", actual, err)
	}
	f, err = findingStore.Get(ctx, orgID, f.ID)
	if err != nil || f.EvidenceVersion != 1 {
		t.Fatalf("evidence version: %+v %v", f, err)
	}
	if _, err := findingStore.Confirm(ctx, orgID, actorID, f.ID, f.Version-1); !errors.Is(err, finding.ErrVersionConflict) {
		t.Fatalf("stale confirmation accepted: %v", err)
	}
	confirmed, err := findingStore.Confirm(ctx, orgID, actorID, f.ID, f.Version)
	if err != nil || confirmed.Status != "confirmed" {
		t.Fatalf("confirm: %+v %v", confirmed, err)
	}
	reports := report.Store{Pool: scheduler.Engine.Pool, Blobs: blobs}
	current, err := reports.Get(ctx, orgID, taskID)
	if err != nil || current.CurrentEvidenceVersion != 2 {
		t.Fatalf("report state: %+v %v", current, err)
	}
	if _, err := reports.Update(ctx, orgID, actorID, taskID, "授权测试结论", 0, 0); !errors.Is(err, report.ErrVersionConflict) {
		t.Fatalf("stale evidence report accepted: %v", err)
	}
	current, err = reports.Update(ctx, orgID, actorID, taskID, "授权测试结论", 0, current.CurrentEvidenceVersion)
	if err != nil || current.Stale {
		t.Fatalf("report update: %+v %v", current, err)
	}
	for _, format := range []string{"markdown", "json", "md-zip"} {
		data, _, err := reports.Export(ctx, orgID, taskID, format)
		if err != nil || len(data) == 0 {
			t.Fatalf("export %s after traffic deletion: %d %v", format, len(data), err)
		}
	}
	retest, err := findingStore.CreateRetest(ctx, orgID, actorID, f.ID)
	if err != nil || retest.EvidenceVersion != 1 {
		t.Fatalf("retest creation: %+v %v", retest, err)
	}
	verificationResponse := []byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
	verification, err := trafficStore.Record(ctx, traffic.RecordInput{OrganizationID: orgID, TaskID: taskID, IntentID: intentID, Method: "GET", URL: "https://example.invalid/private", RemoteIP: "203.0.113.10", StatusCode: 403, RequestHeaders: map[string][]string{}, ResponseHeaders: map[string][]string{}, RequestRaw: []byte("GET /private?retest=1 HTTP/1.1\r\n\r\n"), ResponseRaw: verificationResponse, RequestedAt: time.Now(), RespondedAt: time.Now().Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	verificationEvidence, err := findingStore.BindTraffic(ctx, orgID, actorID, f.ID, verification.ID, "verification", "复测返回 403")
	if err != nil {
		t.Fatal(err)
	}
	verificationEvidence, err = findingStore.UpdateBinding(ctx, orgID, actorID, verificationEvidence.BindingID, "verification", "复测无权限响应")
	if err != nil || verificationEvidence.Note != "复测无权限响应" {
		t.Fatalf("binding metadata update: %+v %v", verificationEvidence, err)
	}
	ordered, err := findingStore.Reorder(ctx, orgID, actorID, f.ID, []string{verificationEvidence.BindingID, evidence.BindingID})
	if err != nil || len(ordered) != 2 || ordered[0].BindingID != verificationEvidence.BindingID {
		t.Fatalf("evidence reorder: %+v %v", ordered, err)
	}
	completed, err := findingStore.CompleteRetest(ctx, orgID, actorID, retest.ID, "inconclusive", "需要进一步确认账户状态", []string{verificationEvidence.BindingID})
	if err != nil || completed.Status != "completed" || completed.Conclusion == nil || *completed.Conclusion != "inconclusive" {
		t.Fatalf("retest completion: %+v %v", completed, err)
	}
	stillConfirmed, err := findingStore.Get(ctx, orgID, f.ID)
	if err != nil || stillConfirmed.Status != "confirmed" {
		t.Fatalf("inconclusive changed status: %+v %v", stillConfirmed, err)
	}
	markdown, _, err := reports.Export(ctx, orgID, taskID, "markdown")
	if err != nil || !bytes.Contains(markdown, []byte("inconclusive")) {
		t.Fatalf("report omitted retest history: %v", err)
	}
	verified, err := backup.Verify(ctx, scheduler.Engine.Pool, blobs.Root)
	if err != nil || verified.EvidenceRefs == 0 || verified.BlobsVerified == 0 {
		t.Fatalf("restored data verification: %+v %v", verified, err)
	}
	responsePath := filepath.Join(blobs.Root, "blobs", (*evidence.ResponseSHA256)[:2], *evidence.ResponseSHA256)
	if err := os.WriteFile(responsePath, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Verify(ctx, scheduler.Engine.Pool, blobs.Root); err == nil {
		t.Fatal("backup verifier accepted tampered evidence")
	}
	if _, err := findingStore.ReadEvidence(ctx, orgID, evidence.BindingID, "response"); !errors.Is(err, blob.ErrCorrupt) {
		t.Fatalf("tampered evidence accepted: %v", err)
	}
	if _, _, err := reports.Export(ctx, orgID, taskID, "md-zip"); !errors.Is(err, blob.ErrCorrupt) {
		t.Fatalf("export accepted corrupt evidence: %v", err)
	}
	if err := findingStore.Unbind(ctx, orgID, actorID, evidence.BindingID); err != nil {
		t.Fatal(err)
	}
	var refs int64
	if err := scheduler.Engine.Pool.QueryRow(ctx, `SELECT evidence_refs FROM blobs WHERE sha256=$1`, *evidence.ResponseSHA256).Scan(&refs); err != nil || refs != 0 {
		t.Fatalf("evidence ref count=%d err=%v", refs, err)
	}
	if _, err := trafficStore.Collect(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("blob collection: %v", err)
	}
	if _, err := os.Stat(responsePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced blob still exists: %v", err)
	}
	current, err = reports.Get(ctx, orgID, taskID)
	if err != nil || !current.Stale {
		t.Fatalf("report should be stale after evidence removal: %+v %v", current, err)
	}
}

func TestBackupVerifierRejectsWrongMasterKey(t *testing.T) {
	scheduler, orgID, _, _ := schedulerFixture(t)
	ctx := context.Background()
	dataDir := t.TempDir()
	store, err := secret.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	secretID := identity.NewID()
	ciphertext, nonce, hint, err := store.Seal(orgID, secretID, "llm_api_key", "fixture-credential")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Engine.Pool.Exec(ctx, `INSERT INTO secret_values(id,organization_id,kind,ciphertext,nonce,hint) VALUES($1,$2,'llm_api_key',$3,$4,$5)`, secretID, orgID, ciphertext, nonce, hint); err != nil {
		t.Fatal(err)
	}
	if report, err := backup.Verify(ctx, scheduler.Engine.Pool, dataDir); err != nil || report.Secrets != 1 {
		t.Fatalf("valid master key rejected: %+v %v", report, err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "secrets", "master.key"), bytes.Repeat([]byte{0x42}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Verify(ctx, scheduler.Engine.Pool, dataDir); err == nil {
		t.Fatal("wrong master key accepted")
	}
}

func TestFailedTrafficTransactionLeavesCollectableOrphans(t *testing.T) {
	scheduler, orgID, _, _ := schedulerFixture(t)
	ctx := context.Background()
	dataDir := t.TempDir()
	store := traffic.Store{Pool: scheduler.Engine.Pool, Blobs: blob.Store{Root: dataDir}}
	request := []byte("GET /orphan HTTP/1.1\r\nHost: example.invalid\r\n\r\n")
	response := []byte("HTTP/1.1 200 OK\r\nContent-Length: 1\r\n\r\na")
	_, err := store.Record(ctx, traffic.RecordInput{
		OrganizationID: orgID, TaskID: identity.NewID(), Method: "GET", URL: "https://example.invalid/orphan", RemoteIP: "203.0.113.10", StatusCode: 200,
		RequestRaw: request, ResponseRaw: response, RequestedAt: time.Now().Add(-time.Second), RespondedAt: time.Now(),
	})
	if err == nil {
		t.Fatal("invalid task unexpectedly accepted")
	}
	for _, raw := range [][]byte{request, response} {
		hash, putErr := store.Blobs.Put(ctx, raw)
		if putErr != nil {
			t.Fatal(putErr)
		}
		var rows int
		if err := scheduler.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM blobs WHERE sha256=$1`, hash).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("uncommitted blob reference: rows=%d err=%v", rows, err)
		}
	}
	if count, err := store.Collect(ctx, time.Now().Add(-time.Hour)); err != nil || count != 0 {
		t.Fatalf("orphan removed before grace period: %d %v", count, err)
	}
	if count, err := store.Collect(ctx, time.Now().Add(time.Hour)); err != nil || count != 2 {
		t.Fatalf("orphan collection: %d %v", count, err)
	}
}
