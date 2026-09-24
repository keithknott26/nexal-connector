package timemachine

import (
	"errors"
	"fmt"
	"strings"
)

// RenderSambaShare returns the managed share stanza used on Linux. It is not a
// complete smb.conf and must be installed by the privileged connector helper.
// macOS must use Apple's native sharing service instead (see internal/lanshare).
func RenderSambaShare(c Config) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	if !c.Enabled {
		return "", errors.New("Time Machine feature is disabled")
	}
	hosts := strings.Join(c.MeshCIDRs, " ")
	return fmt.Sprintf(`[%s]
    path = %s
    browseable = yes
    read only = no
    guest ok = no
    valid users = @nexal-timemachine
    hosts allow = %s
    hosts deny = 0.0.0.0/0 ::/0
    smb encrypt = required
    server signing = mandatory
    vfs objects = catia fruit streams_xattr
    fruit:aapl = yes
    fruit:time machine = yes
    fruit:time machine max size = %d
    fruit:metadata = stream
    fruit:resource = stream
`, c.ShareName, c.MountPath, hosts, c.QuotaBytes), nil
}
