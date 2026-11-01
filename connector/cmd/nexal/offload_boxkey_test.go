package main

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
)

type failPutSecrets struct{ memSecrets }

func (failPutSecrets) Put(context.Context, string, string) error {
	return errors.New("keychain locked")
}

func TestOffloadBoxKeyCreatesOnceAndPersists(t *testing.T) {
	ctx := context.Background()
	s := memSecrets{}
	get := offloadBoxKey(ctx, s)
	k1, err := get()
	if err != nil || k1 == nil {
		t.Fatalf("first use must create a key: %v", err)
	}
	stored := s["offload-box"]
	if stored != base64.RawStdEncoding.EncodeToString(k1.Bytes()) {
		t.Fatal("generated key must be stored base64 under offload-box")
	}
	if k2, _ := get(); k2 != k1 {
		t.Fatal("the same closure must return the cached key")
	}
	// A fresh closure (new process) must load the stored key rather than minting another.
	k3, err := offloadBoxKey(ctx, s)()
	if err != nil || !k3.Equal(k1) {
		t.Fatalf("stored key must be reused: %v", err)
	}
}

func TestOffloadBoxKeyRegeneratesCorruptValue(t *testing.T) {
	ctx := context.Background()
	s := memSecrets{"offload-box": "not-a-valid-key!"}
	k, err := offloadBoxKey(ctx, s)()
	if err != nil || k == nil {
		t.Fatalf("a corrupt stored value must be replaced: %v", err)
	}
	if s["offload-box"] != base64.RawStdEncoding.EncodeToString(k.Bytes()) {
		t.Fatal("replacement key must overwrite the corrupt value")
	}
}

func TestOffloadBoxKeyReportsStoreFailure(t *testing.T) {
	k, err := offloadBoxKey(context.Background(), failPutSecrets{memSecrets{}})()
	if err == nil || k != nil {
		t.Fatalf("a key that cannot be saved must not be handed out: key=%v err=%v", k, err)
	}
}
