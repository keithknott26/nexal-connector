package client

import (
	"context"
	"errors"
)

type Transfer struct {
	ID             string  `json:"id"`
	DonorHostID    string  `json:"donorHostId"`
	ReceiverHostID string  `json:"receiverHostId"`
	PublicKey      string  `json:"publicKey"`
	ExpiresAt      string  `json:"expiresAt"`
	State          string  `json:"state"`
	Ciphertext     *string `json:"ciphertext"`
}

func transferPath(host, id string) (string, error) {
	if !ValidID(host) || (id != "" && !ValidID(id)) {
		return "", errors.New("invalid transfer identity")
	}
	path := "/api/hosts/" + host + "/bundle-transfers"
	if id != "" {
		path += "/" + id
	}
	return path, nil
}

func (c *Client) CreateTransfer(ctx context.Context, receiver, donor, publicKey string) (Transfer, error) {
	var out Transfer
	path, err := transferPath(receiver, "")
	if err != nil || !ValidID(donor) {
		return out, errors.New("invalid transfer participants")
	}
	err = c.call(ctx, "POST", path, map[string]string{"donorHostId": donor, "publicKey": publicKey}, &out)
	return out, err
}
func (c *Client) GetTransfer(ctx context.Context, host, id string) (Transfer, error) {
	var out Transfer
	path, err := transferPath(host, id)
	if err != nil {
		return out, err
	}
	err = c.call(ctx, "GET", path, nil, &out)
	return out, err
}
func (c *Client) PutTransfer(ctx context.Context, host, id, ciphertext string) error {
	path, err := transferPath(host, id)
	if err != nil {
		return err
	}
	var out struct {
		Accepted bool `json:"accepted"`
	}
	if err = c.call(ctx, "PUT", path, map[string]string{"ciphertext": ciphertext}, &out); err != nil {
		return err
	}
	if !out.Accepted {
		return errors.New("transfer not accepted")
	}
	return nil
}
func (c *Client) AckTransfer(ctx context.Context, host, id string) error {
	path, err := transferPath(host, id)
	if err != nil {
		return err
	}
	var out struct {
		Acknowledged bool `json:"acknowledged"`
	}
	if err = c.call(ctx, "POST", path+"/ack", map[string]string{}, &out); err != nil {
		return err
	}
	if !out.Acknowledged {
		return errors.New("transfer not acknowledged")
	}
	return nil
}
