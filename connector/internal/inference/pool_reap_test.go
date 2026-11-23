package inference

import (
	"reflect"
	"testing"
)

func TestOrphanPoolServersPicksOnlyReparentedServersUnderInferenceDir(t *testing.T) {
	ps := `  100     1 /usr/bin/python3 -m mlx_lm server --model /inf/models/a --port 1
  101   555 /usr/bin/python3 -m mlx_lm server --model /inf/models/a --port 2
  102     1 /usr/bin/python3 -m mlx_lm server --model /other/models/a --port 3
  103     1 /usr/bin/python3 -m mlx_lm.shard --model /inf/models/b --pipeline
  104     1 /usr/bin/python3 -m mlx_lm server --model /inf/models/a
  105     1 /usr/bin/python3 -m mlx_lm.generate --model /inf/models/a
garbage line
  abc     1 /usr/bin/python3 -m mlx_lm server --model /inf/models/a --port 4
`
	got := orphanPoolServers(ps, "/inf", 104)
	want := []int{100, 103}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("orphanPoolServers = %v, want %v", got, want)
	}
}

func TestOrphanServeServersJoinsContinuationLinesAndSkipsSelf(t *testing.T) {
	ps := `  200     1 /bin/sh -c python3 - <<EOF
from mlx_lm import server
server.main() --model /inf/models/a/ --port 1
  201     1 /bin/sh -c python3 - <<EOF
from mlx_lm import server
server.main() --model /other/models/a/
  202   777 /bin/sh -c from mlx_lm import server --model /inf/models/a/
  203     1 /bin/sh -c from mlx_lm import server --model /inf/models/a/
  204     1 /usr/bin/python3 -m mlx_lm server --model /inf/models/a/
`
	got := orphanServeServers(ps, "/inf", 203)
	want := []int{200}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("orphanServeServers = %v, want %v", got, want)
	}
}
