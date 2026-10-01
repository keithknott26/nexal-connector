package rosenpass

import (
 "crypto/mlkem"
 "net"
 "strings"
 "testing"
 "golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)
func TestNexalMLKEM1024Manager(t *testing.T){
 key,err:=wgtypes.GenerateKey();if err!=nil{t.Fatal(err)}
 manager,err:=NewManager(nil,"test-interface",key.PublicKey());if err!=nil{t.Fatal(err)}
 if len(manager.GetPubKey())!=1568{t.Fatal("runtime did not generate an ML-KEM-1024 public key")}
 if _,err:=mlkem.NewEncapsulationKey1024(manager.GetPubKey());err!=nil{t.Fatal(err)}
 err=manager.addPeer(make([]byte,524160),net.JoinHostPort(QuantumProfile,"1234"),"127.0.0.1",key.PublicKey().String())
 if err==nil||!strings.Contains(err.Error(),"incompatible peer"){t.Fatal("legacy peer was not explicitly rejected")}
}
