package rosenpass

import (
 "bytes"
 "crypto/mlkem"
 "testing"
)
func TestNexalMLKEM1024(t *testing.T) {
 for _, profile := range []kemProfile{kemStatic,kemEphemeral} {
  pk,sk,err:=generateKeyPair(profile);if err!=nil{t.Fatal(err)}
  if len(pk)!=1568||len(sk)!=64{t.Fatal("incorrect standardized key sizes")}
  kem,err:=newKEM(profile,sk);if err!=nil{t.Fatal(err)}
  ct,secret,err:=kem.EncapSecret(pk);if err!=nil{t.Fatal(err)}
  if len(ct)!=1568||len(secret)!=32{t.Fatal("incorrect ciphertext/shared key sizes")}
  got,err:=kem.DecapSecret(ct);if err!=nil||!bytes.Equal(secret,got){t.Fatal("KEM roundtrip failed")}
  native,err:=mlkem.NewDecapsulationKey1024(sk);if err!=nil{t.Fatal(err)}
  independent,err:=native.Decapsulate(ct);if err!=nil||!bytes.Equal(secret,independent){t.Fatal("stdlib interoperation failed")}
  ct[20]^=1
  rejected,err:=kem.DecapSecret(ct);if err!=nil||bytes.Equal(secret,rejected){t.Fatal("implicit rejection failed")}
  if _,err=kem.DecapSecret(ct[:100]);err==nil{t.Fatal("short ciphertext accepted")}
  if err=validateKeys(pk,sk);err!=nil{t.Fatal(err)}
  other,_,_:=generateKeyPair(profile);if validateKeys(other,sk)==nil{t.Fatal("mismatched key accepted")}
 }
 if ValidatePublicKey(make([]byte,524160))==nil{t.Fatal("legacy McEliece key accepted")}
 if _,_,err:=GenerateRound2KeyPair();err==nil{t.Fatal("legacy export accepted")}
 if initHelloMsgSize+envelopeSize<=1500||respHelloMsgSize+envelopeSize<=1500{t.Fatal("large handshake test no longer exercises datagrams over 1500 bytes")}
}
func TestNexalRejectsOldPacketType(t *testing.T){
 pk,_,err:=GenerateKeyPair();if err!=nil{t.Fatal(err)}
 e:=envelope{payload:&emptyData{}}
 packet:=e.MarshalBinaryAndSeal(pk)
 packet[0]=0x84
 // Even correctly re-sealed legacy packet types are rejected by this profile.
 offset:=len(packet)-macSize-cookieSize
 tag:=khMAC.hash(pk,packet[:offset]);copy(packet[offset:],tag[:macSize])
 if _,err:=e.CheckAndUnmarshalBinary(packet,pk);err==nil{t.Fatal("legacy type accepted")}
}
