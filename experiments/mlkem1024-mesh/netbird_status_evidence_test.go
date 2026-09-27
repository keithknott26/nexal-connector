package status

import (
	"encoding/json"
	"github.com/netbirdio/netbird/client/internal/peer"
	pb "github.com/netbirdio/netbird/client/proto"
	"google.golang.org/protobuf/proto"
	"sync"
	"testing"
	"time"
)

func TestNexalEvidenceSurvivesStatusRPC(t *testing.T) {
	now := time.Now().UTC()
	fs := peer.FullStatus{Peers: []peer.State{{Mux: &sync.RWMutex{}, PubKey: "peer-one", ConnStatus: peer.StatusConnected,
		QuantumProfile: "nexal-mlkem1024-experimental-v1", QuantumKeyInstalledAt: now.Format(time.RFC3339Nano), QuantumKeyExpiresAt: now.Add(3 * time.Minute).Format(time.RFC3339Nano)}}}
	for _, src := range []*pb.FullStatus{ToProtoFullStatus(fs), fs.ToProto()} {
		data, err := proto.Marshal(src)
		if err != nil {
			t.Fatal(err)
		}
		var decoded pb.FullStatus
		if err := proto.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		peers := mapPeers(decoded.Peers, "", nil, nil, nil, "")
		data, err = json.Marshal(peers.Details)
		if err != nil {
			t.Fatal(err)
		}
		var output []map[string]any
		if err := json.Unmarshal(data, &output); err != nil {
			t.Fatal(err)
		}
		if len(output) != 1 || output[0]["quantumProfile"] != fs.Peers[0].QuantumProfile || output[0]["quantumKeyInstalledAt"] != fs.Peers[0].QuantumKeyInstalledAt || output[0]["quantumKeyExpiresAt"] != fs.Peers[0].QuantumKeyExpiresAt {
			t.Fatal("status lost installation evidence")
		}
	}
}
