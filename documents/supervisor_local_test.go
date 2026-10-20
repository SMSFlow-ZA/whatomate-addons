package documents

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in operational acceptance. The supplied private corpus must have been
// freshly provisioned with official freshclam TLS/signature validation. This
// test never downloads definitions, weakens readiness, or supplies fake scans.
func TestRealSupervisorWithOfficialDefinitions(t *testing.T) {
	image, root, defs := os.Getenv("DOCUMENTS_TEST_IMAGE"), os.Getenv("DOCUMENTS_TEST_JOB_ROOT"), os.Getenv("DOCUMENTS_TEST_DEFINITIONS")
	if image == "" || root == "" || defs == "" {
		t.Skip("isolated controller, pinned worker image and official corpus not configured")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	// Leave each one-job replay marker/receipt for operator inspection.
	jobRoot, err := os.MkdirTemp(root, "acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := DefinitionsDigest(defs)
	if err != nil {
		t.Fatal(err)
	}
	c := testConfig(t)
	c.QuarantineRoot = "/tmp/quarantine"
	c.DatabasePath = "/var/lib/clamav"
	c.CommandTimeout = 30 * time.Second
	c.JobTimeout = 2 * time.Minute
	c.ScannerRevision = "clamav-1.4.4-official-corpus-local-acceptance"
	cfg := SupervisorConfig{JobRoot: jobRoot, DockerPath: "/usr/local/bin/docker", Image: image, DefinitionsRoot: defs, DefinitionsHash: digest, Worker: c, MaxConcurrent: 1, Timeout: 3 * time.Minute, MemoryMiB: 1024, CPUs: 1, PIDs: 64, TmpfsMiB: 128}
	s, err := NewSupervisor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	j := supervisorJob("official-clean")
	j.Input = syntheticPDF()
	j.InputHash = hash(j.Input)
	out, receipt, err := s.Process(context.Background(), j)
	if err != nil || !strings.Contains(out.Document.Pages[0].Text, "Synthetic document worker text.") || receipt.DefinitionsHash != digest {
		t.Fatalf("real sandboxed official-corpus job: %v %+v", err, receipt)
	}
	if _, err := os.Stat(filepath.Join(jobRoot, hash([]byte(j.TenantID+"\x00"+j.ID)), "receipt.json")); err != nil {
		t.Fatal(err)
	}
	// Standard EICAR antivirus test marker, never live malware.
	j.ID = "official-eicar"
	j.Input = syntheticPDFWithEICARAttachment()
	j.InputHash = hash(j.Input)
	if out, receipt, err = s.Process(context.Background(), j); err == nil || out.Evidence.MalwareClean || receipt.OutputHash != "" {
		t.Fatal("official-corpus EICAR detection did not quarantine")
	}
	t.Logf("real offline sandbox acceptance image=%s definitions=%s job_root=%s", image, digest, jobRoot)
}

func syntheticPDFWithEICARAttachment() []byte {
	marker := "X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*"
	content := "BT /F1 24 Tf 72 720 Td (Synthetic attached antivirus fixture.) Tj ET\n"
	objects := []string{
		`<< /Type /Catalog /Pages 2 0 R /Names << /EmbeddedFiles << /Names [(eicar.com) 6 0 R] >> >> >>`,
		`<< /Type /Pages /Kids [3 0 R] /Count 1 >>`,
		`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>`,
		`<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>`,
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
		`<< /Type /Filespec /F (eicar.com) /EF << /F 7 0 R >> >>`,
		fmt.Sprintf("<< /Type /EmbeddedFile /Length %d >>\nstream\n%s\nendstream", len(marker), marker),
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, obj := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return b.Bytes()
}
