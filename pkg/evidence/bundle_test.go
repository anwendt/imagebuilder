package evidence

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

const (
	testSBOM  = `{"bomFormat":"CycloneDX","specVersion":"1.6","components":[]}`
	testTrivy = `{"SchemaVersion":2,"Results":[{"Target":"ubuntu","Vulnerabilities":[{"VulnerabilityID":"CVE-1","Severity":"LOW"}]}]}`
)

type tarEntry struct {
	name     string
	body     string
	typeflag byte
}

func buildTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o600, Size: int64(len(e.body)), Typeflag: e.typeflag}
		if e.typeflag == 0 {
			hdr.Typeflag = tar.TypeReg
		}
		if hdr.Typeflag == tar.TypeDir || hdr.Typeflag == tar.TypeSymlink {
			hdr.Size = 0
			hdr.Linkname = e.body
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func validEntries() []tarEntry {
	return []tarEntry{
		{name: "evidence/", typeflag: tar.TypeDir},
		{name: "evidence/sbom.cdx.json", body: testSBOM},
		{name: "evidence/trivy-report.json", body: testTrivy},
		{name: "evidence/collector.json", body: `{"syft":"1.30.0","trivy":"0.70.0"}`},
	}
}

func TestValidateBundle(t *testing.T) {
	tests := []struct {
		name    string
		data    func(t *testing.T) []byte
		wantErr string
	}{
		{name: "valid", data: func(t *testing.T) []byte { return buildTarGz(t, validEntries()) }},
		{name: "valid without collector and with dot prefix", data: func(t *testing.T) []byte {
			return buildTarGz(t, []tarEntry{
				{name: "./evidence/sbom.cdx.json", body: testSBOM},
				{name: "./evidence/trivy-report.json", body: testTrivy},
			})
		}},
		{name: "empty", data: func(*testing.T) []byte { return nil }, wantErr: "is empty"},
		{name: "too large", data: func(*testing.T) []byte {
			b := make([]byte, MaxBundleBytes+1)
			_, _ = rand.Read(b)
			return b
		}, wantErr: "exceeds"},
		{name: "not gzip", data: func(*testing.T) []byte { return []byte("plain") }, wantErr: "not a gzip"},
		{name: "missing sbom", data: func(t *testing.T) []byte {
			return buildTarGz(t, validEntries()[2:])
		}, wantErr: "sbom.cdx.json is missing"},
		{name: "missing trivy report", data: func(t *testing.T) []byte {
			e := validEntries()
			return buildTarGz(t, []tarEntry{e[0], e[1]})
		}, wantErr: "trivy-report.json is missing"},
		{name: "invalid sbom json", data: func(t *testing.T) []byte {
			e := validEntries()
			e[1].body = "{not json"
			return buildTarGz(t, e)
		}, wantErr: "sbom.cdx.json is not valid JSON"},
		{name: "sbom not cyclonedx", data: func(t *testing.T) []byte {
			e := validEntries()
			e[1].body = `{"spdxVersion":"SPDX-2.3"}`
			return buildTarGz(t, e)
		}, wantErr: "not a CycloneDX"},
		{name: "trivy report not trivy", data: func(t *testing.T) []byte {
			e := validEntries()
			e[2].body = `{"Results":[]}`
			return buildTarGz(t, e)
		}, wantErr: "SchemaVersion"},
		{name: "invalid collector", data: func(t *testing.T) []byte {
			e := validEntries()
			e[3].body = `[1`
			return buildTarGz(t, e)
		}, wantErr: "collector.json is not valid JSON"},
		{name: "path outside outbox", data: func(t *testing.T) []byte {
			return buildTarGz(t, append(validEntries(), tarEntry{name: "etc/passwd", body: "x"}))
		}, wantErr: "outside the evidence outbox"},
		{name: "path traversal", data: func(t *testing.T) []byte {
			return buildTarGz(t, append(validEntries(), tarEntry{name: "evidence/../../x", body: "x"}))
		}, wantErr: "outside the evidence outbox"},
		{name: "unknown file", data: func(t *testing.T) []byte {
			return buildTarGz(t, append(validEntries(), tarEntry{name: "evidence/notes.txt", body: "x"}))
		}, wantErr: "unexpected file"},
		{name: "symlink", data: func(t *testing.T) []byte {
			return buildTarGz(t, append(validEntries(), tarEntry{name: "evidence/link", body: "/etc/shadow", typeflag: tar.TypeSymlink}))
		}, wantErr: "not a regular file"},
		{name: "duplicate file", data: func(t *testing.T) []byte {
			return buildTarGz(t, append(validEntries(), tarEntry{name: "evidence/sbom.cdx.json", body: testSBOM}))
		}, wantErr: "more than once"},
		{name: "uncompressed bomb", data: func(t *testing.T) []byte {
			e := validEntries()
			e[3].body = `{"pad":"` + strings.Repeat("a", int(MaxUncompressedFileBytes)) + `"}`
			return buildTarGz(t, e)
		}, wantErr: "larger than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := tt.data(t)
			bundle, err := ValidateBundle(data)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ValidateBundle() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateBundle() error = %v", err)
			}
			sum := sha256.Sum256(data)
			if bundle.SHA256 != hex.EncodeToString(sum[:]) {
				t.Fatalf("SHA256 = %q", bundle.SHA256)
			}
			if string(bundle.SBOM) != testSBOM || string(bundle.TrivyReport) != testTrivy {
				t.Fatalf("bundle files not extracted: %+v", bundle)
			}
		})
	}
}

func TestVerifyBundleDigest(t *testing.T) {
	data := buildTarGz(t, validEntries())
	sum := sha256.Sum256(data)
	good := hex.EncodeToString(sum[:])
	tests := []struct {
		name    string
		digest  string
		wantErr bool
	}{
		{name: "match", digest: good},
		{name: "match with prefix", digest: "sha256:" + good},
		{name: "mismatch", digest: strings.Repeat("0", 64), wantErr: true},
		{name: "empty", digest: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := VerifyBundle(data, tt.digest)
			if (err != nil) != tt.wantErr {
				t.Fatalf("VerifyBundle() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckSeverities(t *testing.T) {
	report := []byte(`{"SchemaVersion":2,"Results":[
	 {"Target":"a","Vulnerabilities":[{"VulnerabilityID":"CVE-1","Severity":"HIGH","FixedVersion":"1.2"},{"VulnerabilityID":"CVE-2","Severity":"LOW","FixedVersion":"1"},{"VulnerabilityID":"CVE-4","Severity":"HIGH"}]},
	 {"Target":"b","Vulnerabilities":[{"VulnerabilityID":"CVE-3","Severity":"CRITICAL","FixedVersion":"2.0"},{"VulnerabilityID":"CVE-5","Severity":"CRITICAL","FixedVersion":""}]},
	 {"Target":"c"}]}`)
	tests := []struct {
		name    string
		failOn  []string
		report  []byte
		wantErr string
	}{
		{name: "no policy", report: report},
		{name: "high and critical", failOn: []string{"HIGH", "CRITICAL"}, report: report, wantErr: "2 fixable vulnerabilities"},
		{name: "critical only", failOn: []string{"critical"}, report: report, wantErr: "1 fixable vulnerabilities"},
		{name: "medium none", failOn: []string{"MEDIUM"}, report: report},
		{name: "broken report", failOn: []string{"HIGH"}, report: []byte("{"), wantErr: "parse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckSeverities(tt.report, tt.failOn)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("CheckSeverities() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("CheckSeverities() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestPackRoundTrip(t *testing.T) {
	data, err := Pack(map[string][]byte{
		SBOMFile:  []byte(testSBOM),
		TrivyFile: []byte(testTrivy),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateBundle(data); err != nil {
		t.Fatalf("packed bundle invalid: %v", err)
	}
	if _, err := Pack(map[string][]byte{"../x": nil}); err == nil {
		t.Fatal("Pack accepted an invalid name")
	}
}
