package main

import (
	"context"
	"crypto/mlkem"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"nexal/connector/internal/bundletransfer"
	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
)

func bundleCommand(ctx context.Context, command string, args []string) error {
	f, path, err := flags(command)
	if err != nil {
		return err
	}
	donor := f.String("from-host", "", "enrolled donor host ID")
	transfer := f.String("transfer", "", "receiver's public transfer ID")
	bundle := f.String("bundle", "", "absolute donor client folder")
	fingerprint := f.String("receiver-key-sha256", "", "public key fingerprint displayed on receiver")
	pathOnly := f.Bool("path-only", false, "output only the installed receiver folder")
	if err := parse(f, args, path); err != nil {
		return err
	}
	if command == "bundle-send" {
		if !client.ValidID(*transfer) || !filepath.IsAbs(*bundle) || len(*fingerprint) != 64 ||
			*donor != "" || *pathOnly {
			return errors.New("bundle-send requires --transfer, --bundle and --receiver-key-sha256")
		}
	} else if !client.ValidID(*donor) || *transfer != "" || *bundle != "" || *fingerprint != "" {
		return errors.New("bundle-receive requires --from-host; sender flags are not allowed")
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if !client.ValidID(c.HostID) {
		return errors.New("enroll this Mac in the shared coordinator first")
	}
	if *donor == c.HostID {
		return errors.New("donor and receiver must be different enrolled hosts")
	}
	secrets, err := config.NewSecrets(*path, c)
	if err != nil {
		return err
	}
	token, err := secrets.Get(ctx, "host")
	if err != nil {
		return err
	}
	api, err := client.New(c.Coordinator, token, c.Development)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if command == "bundle-send" {
		t, err := api.GetTransfer(ctx, c.HostID, *transfer)
		if err != nil {
			return err
		}
		if t.ID != *transfer || t.DonorHostID != c.HostID || t.State != "pending" {
			return errors.New("transfer is not pending for this donor")
		}
		b, err := bundletransfer.Read(*bundle)
		if err != nil {
			return err
		}
		defer func() {
			for _, content := range b {
				clear(content)
			}
		}()
		encrypted, err := bundletransfer.Seal(t, *fingerprint, b)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Sending only pager credentials to enrolled receiver %s.\n", t.ReceiverHostID)
		if err := api.PutTransfer(ctx, c.HostID, t.ID, encrypted); err != nil {
			return errors.New("upload acknowledgement unavailable; receiver may still receive it; do not blindly replace the transfer")
		}
		return emit(map[string]any{"uploaded": true, "transferId": t.ID, "receiverHostId": t.ReceiverHostID})
	}
	key, err := mlkem.GenerateKey768()
	if err != nil {
		return errors.New("cannot generate receiver encryption key")
	}
	public := key.EncapsulationKey().Bytes()
	t, err := api.CreateTransfer(ctx, c.HostID, *donor, base64.StdEncoding.EncodeToString(public))
	if err != nil {
		return err
	}
	if !client.ValidID(t.ID) || t.ReceiverHostID != c.HostID || t.DonorHostID != *donor ||
		t.PublicKey != base64.StdEncoding.EncodeToString(public) || t.State != "pending" {
		return errors.New("unexpected transfer response")
	}
	fmt.Fprintf(os.Stderr, "Receiver host: %s\nDonor host: %s\nTransfer ID: %s\nReceiver key SHA-256: %s\n"+
		"Keep this receiver terminal open (up to 10 minutes). Copy these PUBLIC values to the donor.\n",
		c.HostID, *donor, t.ID, bundletransfer.Fingerprint(public))
	original := t
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return errors.New("receiver stopped or transfer timed out; start a fresh request")
		case <-ticker.C:
		}
		t, err = api.GetTransfer(ctx, c.HostID, original.ID)
		if err != nil {
			return err
		}
		if t.ID != original.ID || t.DonorHostID != original.DonorHostID ||
			t.ReceiverHostID != original.ReceiverHostID || t.PublicKey != original.PublicKey ||
			t.ExpiresAt != original.ExpiresAt {
			return errors.New("transfer identity changed")
		}
		if t.State == "pending" {
			continue
		}
		if t.State != "ready" {
			return errors.New("transfer is no longer available")
		}
		b, err := bundletransfer.Open(t, key)
		if err != nil {
			return err
		}
		defer func() {
			for _, content := range b {
				clear(content)
			}
		}()
		home, err := os.UserConfigDir()
		if err != nil {
			return errors.New("cannot locate application support directory")
		}
		parent := filepath.Join(home, "Nexal Pager Lab")
		if err := os.MkdirAll(parent, 0700); err != nil {
			return errors.New("cannot create private receive parent")
		}
		dir, err := bundletransfer.Install(parent, b)
		if err != nil {
			return err
		}
		acknowledged := api.AckTransfer(ctx, c.HostID, t.ID) == nil
		if !acknowledged {
			fmt.Fprintln(os.Stderr, "Bundle saved locally; coordinator acknowledgement failed. Encrypted relay expires automatically.")
		}
		if *pathOnly {
			_, err = fmt.Fprintln(os.Stdout, dir)
			return err
		}
		return emit(map[string]any{"bundlePath": dir, "received": true, "acknowledged": acknowledged})
	}
}
