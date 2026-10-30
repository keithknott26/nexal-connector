package lanshare

import "testing"

func TestStepStringRendersACopyableCommand(t *testing.T) {
	cases := []struct {
		name string
		step Step
		want string
	}{
		{"plain unprivileged", Step{Argv: []string{"/bin/ls", "-l"}}, "/bin/ls -l"},
		{"privileged gets sudo", Step{Argv: []string{"/usr/bin/true"}, NeedsRoot: true}, "sudo /usr/bin/true"},
		{"space is quoted", Step{Argv: []string{"/usr/sbin/sharing", "-a", "/Users/me/Time Machine"}}, "/usr/sbin/sharing -a '/Users/me/Time Machine'"},
		{"single quote is escaped", Step{Argv: []string{"/bin/echo", "it's"}}, `/bin/echo 'it'\''s'`},
		{"dollar is quoted", Step{Argv: []string{"/bin/echo", "$HOME"}}, "/bin/echo '$HOME'"},
		{"no argv", Step{}, ""},
	}
	for _, c := range cases {
		if got := c.step.String(); got != c.want {
			t.Errorf("%s: String() = %q, want %q", c.name, got, c.want)
		}
	}
}
