package remoteinference

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerLogIsNilWithoutUsableLogDir(t *testing.T) {
	if (&Runner{}).serverLog(9000) != nil {
		t.Fatal("empty LogDir must disable the server log")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if (&Runner{LogDir: filepath.Join(file, "sub")}).serverLog(9000) != nil {
		t.Fatal("an uncreatable LogDir must disable the server log")
	}
}

func TestServerLogWritesPerPortFileInLogDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	l := (&Runner{LogDir: dir}).serverLog(8123)
	if l == nil {
		t.Fatal("expected a rotating log")
	}
	if _, err := l.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "inference-8123.log"))
	if err != nil || string(data) != "hello\n" {
		t.Fatalf("log file = %q, %v", data, err)
	}
}

func TestReportPSanitisesDetailAndCarriesProgress(t *testing.T) {
	c := &fakeCoord{}
	r := &Runner{Coord: c, HostID: "h", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	p := 0.5
	r.reportP(context.Background(), Task{ID: "t1"}, "running", "download", "line1\nline2\t"+strings.Repeat("x", 400), "ep", &p)
	got := c.last()
	if got.TaskID != "t1" || got.State != "running" || got.Step != "download" || got.Endpoint != "ep" {
		t.Fatalf("report = %+v", got)
	}
	if got.Progress == nil || *got.Progress != 0.5 {
		t.Fatalf("progress = %v", got.Progress)
	}
	if strings.ContainsAny(got.Detail, "\n\t") || len(got.Detail) > 280 {
		t.Fatalf("detail not cleaned: %q", got.Detail)
	}
}
