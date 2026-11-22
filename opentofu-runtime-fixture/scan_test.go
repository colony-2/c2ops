package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
)

func TestScanRejectsPrivateBytesAndArchiveMembers(t *testing.T) {
	private := []byte("private-token-sentinel")
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if e := tw.WriteHeader(&tar.Header{Name: "nested", Size: int64(len(private)), Mode: 0600, Format: tar.FormatUSTAR}); e != nil {
		t.Fatal(e)
	}
	if _, e := tw.Write(private); e != nil {
		t.Fatal(e)
	}
	if e := tw.Close(); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"chapter byte field", jobdb.JobStartChapter{Input: jobdb.ApplicationInputBytes{Data: private}}},
		{"artifact tar member", archive.Bytes()},
		{"encoded byte field", base64.StdEncoding.EncodeToString(private)},
		{"plaintext state", []byte(`{"serial":1,"lineage":"fixture","resources":[]}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := scanner{referenced: map[string]bool{}}
			if e := s.value(reflect.ValueOf(tc.value), 0); e == nil {
				t.Fatal("scanner accepted private content")
			}
		})
	}
}

// Exercise enumeration and artifact opening against retained storage as well as
// testing the decoder directly; a clean report projection cannot satisfy this.
func TestBackingScannerRejectsPersistedInputAndArtifact(t *testing.T) {
	for _, kind := range []string{"input", "artifact"} {
		t.Run(kind, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			rt, e := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "history.db"), SecretProvider: &provider{}})
			if e != nil {
				t.Fatal(e)
			}
			defer rt.Close(ctx)
			_, e = rt.CreateCell(ctx, jobdb.CreateCellRequest{TenantID: "scan", Name: "fixture", Repo: "github.com/opentofu-fixture/config"})
			if e != nil {
				t.Fatal(e)
			}
			in := map[string]any{"public": true}
			var artifacts []jobdb.Artifact
			if kind == "input" {
				in["bytes"] = []byte("private-token-sentinel")
			} else {
				artifacts = append(artifacts, jobdb.NewArtifactFromBytes("private.bin", []byte("private-token-sentinel")))
			}
			data, e := jobdb.NewTaskData(in, artifacts...)
			if e != nil {
				t.Fatal(e)
			}
			handle, e := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "scan", Cell: "fixture", CellRepo: "github.com/opentofu-fixture/config", JobType: "scanner-control", Data: data}})
			if e != nil {
				t.Fatal(e)
			}
			if _, _, e = scanHistory(ctx, rt, handle.JobKey, ""); e == nil || !strings.Contains(e.Error(), "private sentinel") {
				t.Fatalf("scanner did not detect deliberately leaked backing content: %v", e)
			}
		})
	}
}
