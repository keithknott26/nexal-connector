package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"nexal/connector/internal/config"
	"nexal/connector/internal/drive"
)

// `nexal drive` is the owner's interface to Nexal Drive.
//
// WHY IT ENCRYPTS ON THIS SIDE: the coordinator enforces the quota, indexes the
// keys and meters the sizes, so it necessarily sees all three. It does NOT need
// to see the contents, and this command makes sure it cannot. Bodies are sealed
// with ML-KEM-768 to a key that exists only in this Mac's private configuration
// directory, so the coordinator, the R2 bucket, and anyone who later obtains the
// stored bytes hold ciphertext they cannot open -- including an adversary who
// keeps it until RSA and ECC fall, because neither ever protected it.
//
// WHAT IS STILL VISIBLE, said plainly because a claim of privacy that overstates
// itself is worse than none: the object's key (its path), its size, and the time
// it was written are all visible to the coordinator by construction. Contents
// are not.
//
// THE KEY IS NOT ESCROWED. drive-key.json in the configuration directory is the
// only thing that can decrypt this Mac's objects; the coordinator cannot recover
// it. Losing that file loses the data. `nexal drive key` prints where it is so
// it can be backed up somewhere other than the drive it protects.
func driveCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: nexal drive put|get|list|usage|key [flags] [--config absolute-path]")
	}
	switch args[0] {
	case "put":
		return drivePut(ctx, args[1:])
	case "get":
		return driveGet(ctx, args[1:])
	case "list":
		return driveList(ctx, args[1:])
	case "usage":
		return driveUsage(ctx, args[1:])
	case "key":
		return driveKeyInfo(args[1:])
	default:
		return fmt.Errorf("unknown drive subcommand %q", args[0])
	}
}

// driveClient loads the configuration and the host credential.
//
// No configuration lock is taken. Unlike `peers accept`, none of these
// subcommands mutates local state that a concurrent run could lose: the drive
// key is created once and then only read, and the ledger is the coordinator's.
// Taking the exclusive lock here would block `nexal run` for the duration of a
// multi-minute upload for no benefit.
func driveClient(ctx context.Context, path string) (*drive.Client, config.Config, error) {
	c, err := config.Load(path)
	if err != nil {
		return nil, config.Config{}, err
	}
	if c.HostID == "" {
		return nil, config.Config{}, errors.New("this Mac is not enrolled, so it has no host credential for the drive; run `nexal enroll` first")
	}
	secrets, err := config.NewSecrets(path, c)
	if err != nil {
		return nil, config.Config{}, err
	}
	token, err := secrets.Get(ctx, "host")
	if err != nil {
		return nil, config.Config{}, err
	}
	api, err := drive.NewClient(c.Coordinator, token, c.Development)
	if err != nil {
		return nil, config.Config{}, err
	}
	return api, c, nil
}

func drivePut(ctx context.Context, args []string) error {
	f, path, err := flags("drive put")
	if err != nil {
		return err
	}
	key := f.String("key", "", "drive key to store the object under")
	file := f.String("file", "", "absolute path of the file to upload")
	if err := parse(f, args, path); err != nil {
		return err
	}
	if *key == "" || *file == "" {
		return errors.New("both --key and --file are required")
	}
	if !filepath.IsAbs(*file) {
		return errors.New("--file must be an absolute path")
	}
	// Validate the key before reading the file: the key is the AEAD associated
	// data, so a key this client would accept and the coordinator would not is a
	// disagreement that must surface before anything is encrypted under it.
	if err := drive.ValidateKey(*key); err != nil {
		return err
	}
	info, err := os.Stat(*file)
	if err != nil {
		return fmt.Errorf("cannot read %s", *file)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory; upload individual files", *file)
	}
	// Refuse on the stat rather than after reading the whole file into memory.
	if info.Size() > drive.MaxPlaintextBytes {
		return fmt.Errorf("%s is %d bytes; the largest object this drive accepts is %d bytes of content (the remaining %d bytes of the %d-byte object are encryption overhead). Chunked upload is not implemented",
			*file, info.Size(), drive.MaxPlaintextBytes, drive.Overhead, drive.MaxObjectBytes)
	}
	plain, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("cannot read %s", *file)
	}
	defer clear(plain)
	// Re-check after the read: the file may have grown between stat and read.
	if len(plain) > drive.MaxPlaintextBytes {
		return fmt.Errorf("%s grew past the %d-byte content limit while being read", *file, drive.MaxPlaintextBytes)
	}

	api, _, err := driveClient(ctx, *path)
	if err != nil {
		return err
	}
	private, err := drive.LoadOrCreateKey(*path)
	if err != nil {
		return err
	}
	sealed, err := drive.Seal(drive.PublicKey(private), *key, plain)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	object, err := api.Put(ctx, *key, sealed)
	if err != nil {
		return err
	}
	return emit(map[string]any{
		"stored": map[string]any{
			"key":             object.Key,
			"contentBytes":    len(plain),
			"storedBytes":     object.SizeBytes,
			"encryption":      "ML-KEM-768 + AES-256-GCM",
			"createdAt":       object.CreatedAt,
			"coordinatorSees": []string{"key", "size", "time"},
		},
		"note": "the coordinator stores ciphertext only; the key that opens it is in " + drive.KeyPath(*path) + " and is not escrowed",
	})
}

func driveGet(ctx context.Context, args []string) error {
	f, path, err := flags("drive get")
	if err != nil {
		return err
	}
	key := f.String("key", "", "drive key to fetch")
	out := f.String("out", "", "absolute path to write the decrypted contents to")
	if err := parse(f, args, path); err != nil {
		return err
	}
	if *key == "" || *out == "" {
		return errors.New("both --key and --out are required")
	}
	if !filepath.IsAbs(*out) {
		return errors.New("--out must be an absolute path")
	}
	if err := drive.ValidateKey(*key); err != nil {
		return err
	}
	// Refuse to overwrite. A restore that silently replaced a file the owner
	// still wanted would be a data-loss bug in the tool whose job is preventing
	// data loss.
	if _, err := os.Lstat(*out); err == nil {
		return fmt.Errorf("%s already exists; choose a path that does not", *out)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot write to %s", *out)
	}

	api, _, err := driveClient(ctx, *path)
	if err != nil {
		return err
	}
	private, err := drive.LoadOrCreateKey(*path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	sealed, err := api.Get(ctx, *key)
	if err != nil {
		return err
	}
	plain, err := drive.Open(private, *key, sealed)
	if err != nil {
		return err
	}
	defer clear(plain)
	// 0600 through the hardened writer: decrypted contents must not land
	// world-readable, and must not be written through a symlink an attacker
	// planted at the destination.
	if err := config.AtomicPrivate(*out, plain); err != nil {
		return fmt.Errorf("cannot write %s: %w", *out, err)
	}
	return emit(map[string]any{
		"restored": map[string]any{"key": *key, "path": *out, "contentBytes": len(plain), "mode": "0600"},
	})
}

func driveList(ctx context.Context, args []string) error {
	f, path, err := flags("drive list")
	if err != nil {
		return err
	}
	cursor := f.String("cursor", "", "opaque cursor from a previous response")
	limit := f.Int("limit", 0, "maximum objects to return")
	if err := parse(f, args, path); err != nil {
		return err
	}
	api, _, err := driveClient(ctx, *path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	objects, next, err := api.List(ctx, *cursor, *limit)
	if err != nil {
		return err
	}
	rows := make([]map[string]any, 0, len(objects))
	for _, o := range objects {
		rows = append(rows, map[string]any{
			"key": o.Key, "storedBytes": o.SizeBytes, "createdAt": o.CreatedAt,
		})
	}
	return emit(map[string]any{"count": len(rows), "objects": rows, "nextCursor": next})
}

func driveUsage(ctx context.Context, args []string) error {
	f, path, err := flags("drive usage")
	if err != nil {
		return err
	}
	if err := parse(f, args, path); err != nil {
		return err
	}
	api, _, err := driveClient(ctx, *path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	usage, err := api.Usage(ctx)
	if err != nil {
		return err
	}
	// Report stored bytes, which is what the quota counts. Content is smaller by
	// the per-object overhead, and saying "used" without saying which would
	// mislead an owner comparing this against their files.
	remaining := usage.CeilingBytes - usage.UsedBytes
	if remaining < 0 {
		remaining = 0
	}
	return emit(map[string]any{
		"tier": usage.Tier, "ceilingBytes": usage.CeilingBytes,
		"usedBytes": usage.UsedBytes, "remainingBytes": remaining,
		"objectCount": usage.ObjectCount, "updatedAt": usage.UpdatedAt,
		"note": "usedBytes counts stored ciphertext, which exceeds the content size by " +
			fmt.Sprint(drive.Overhead) + " bytes per object",
	})
}

// driveKeyInfo reports where the key lives and whether it exists yet. It never
// prints the key itself: a command that writes a decryption key to stdout will
// eventually have that key in a shell history, a CI log, or a screenshot.
func driveKeyInfo(args []string) error {
	f, path, err := flags("drive key")
	if err != nil {
		return err
	}
	if err := parse(f, args, path); err != nil {
		return err
	}
	keyPath := drive.KeyPath(*path)
	exists := true
	if _, err := os.Lstat(keyPath); errors.Is(err, os.ErrNotExist) {
		exists = false
	}
	return emit(map[string]any{
		"path":       keyPath,
		"exists":     exists,
		"algorithm":  "ML-KEM-768 (FIPS 203) sealing an AES-256-GCM content key",
		"escrowed":   false,
		"warning":    "this file is the only thing that can decrypt this Mac's drive objects; the coordinator cannot recover it. Back it up somewhere other than this drive.",
		"neverPrint": "the key material is deliberately not included in this output",
	})
}
