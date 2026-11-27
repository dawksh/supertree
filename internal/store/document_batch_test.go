package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dawksh/supertree/internal/command"
	"github.com/dawksh/supertree/internal/store"
)

func TestCompoundDocumentsRollBackOnStaleRevisionAndCancellation(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	batch := []store.DocumentUpdate{{Kind: "native-buffer-v1", ID: "a", Value: "original-a"}, {Kind: "native-buffer-v1", ID: "b", Value: "original-b"}}
	if _, err = s.PutDocuments(ctx, batch); err != nil {
		t.Fatal(err)
	}
	batch[0].ExpectedRevision, batch[0].Value = 1, "changed-a"
	batch[1].ExpectedRevision, batch[1].Value = 0, "changed-b"
	if _, err = s.PutDocuments(ctx, batch); err == nil {
		t.Fatal("stale compound edit accepted")
	} else {
		var coded *command.Error
		if !errors.As(err, &coded) || coded.Code != "conflict" {
			t.Fatal(err)
		}
	}
	docs, err := s.Documents(ctx, "native-buffer-v1")
	if err != nil || len(docs) != 2 || docs[0].Revision != 1 || string(docs[0].Data) != `"original-a"` || docs[1].Revision != 1 {
		t.Fatalf("partial edit committed: %#v %v", docs, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	batch[1].ExpectedRevision = 1
	if _, err = s.PutDocuments(cancelled, batch); err == nil {
		t.Fatal("cancelled edit committed")
	}
	batch[1] = batch[0]
	if _, err = s.PutDocuments(ctx, batch); err == nil {
		t.Fatal("duplicate edit accepted")
	}
	docs, err = s.Documents(ctx, "native-buffer-v1")
	if err != nil || docs[0].Revision != 1 {
		t.Fatal("rejected edit changed revision")
	}
}
