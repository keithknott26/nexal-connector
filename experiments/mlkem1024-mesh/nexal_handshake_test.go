package rosenpass_test

import (
 "testing"
 rp "cunicu.li/go-rosenpass"
)
// Upstream harness covers mutual output-key equality, four rotations and expiry.
func TestNexalHandshake(t *testing.T){
 testHandshake(t,newGoServer,newGoServer,rp.GenerateKeyPair,rp.GenerateKeyPair,4)
}
