package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Managed mode: dev containers for MANY tenants on one Linux server ("neXal
// storage", docs/NEXAL-CLOUD-HOSTS.md in nexal-platform; install steps in
// connector/deploy/nexal-storage/README.md).
//
// The coordinator tags every task for a managed host with an opaque per-tenant
// tag (Task.Tenant, 16 hex). Here that tag gives each tenant:
//
//   - its own docker bridge network (ManagedNetworkName), so one tenant's
//     containers can never reach another tenant's on the server (docker isolates
//     bridge networks from each other; the host firewall in the README also stops
//     them reaching the server itself and the LAN);
//   - its own cgroup slice (ManagedSliceName) under nexal-tenants.slice, plus
//     per-container CPU, memory (no swap) and pids limits;
//   - labels (nexal.tenant, nexal.workspace) for inventory and cleanup;
//   - a devcontainer.json rewritten through an allowlist (SanitizeManagedDevcontainer):
//     no host mounts, no privileged mode or extra capabilities, no runArgs, no
//     initializeCommand (it would run ON THE SERVER), no docker compose.
//
// Repository sources are cloned by the connector itself (no hooks, no submodules)
// so their devcontainer.json passes through the same allowlist; DevPod is then
// pointed at the local, sanitized copy.

// ManagedConfig turns managed mode on for a DevPod backend.
type ManagedConfig struct {
	// SlicePrefix is the parent of the per-tenant slices; "" disables
	// --cgroup-parent (e.g. a docker daemon on the cgroupfs driver).
	SlicePrefix string
	// StorageOpt adds --storage-opt size=<disk>G (needs overlay2 on xfs with pquota).
	StorageOpt bool
	// PidsLimit per dev container; 0 means 4096.
	PidsLimit int
}

// DefaultManagedConfig is what the Linux wiring uses unless the environment says otherwise.
func DefaultManagedConfig() ManagedConfig {
	return ManagedConfig{SlicePrefix: "nexal-tenants", PidsLimit: 4096}
}

// ManagedNetworkName is the tenant's docker network.
func ManagedNetworkName(tenant string) string { return "nexal-t-" + tenant }

// ManagedBridgeName is the tenant network's Linux bridge (15 characters max).
// Every one starts with "nx-", which the server firewall matches as "nx-+".
func ManagedBridgeName(tenant string) string { return "nx-" + tenant[:12] }

// ManagedSliceName is the tenant's systemd slice (a child of <prefix>.slice).
func ManagedSliceName(prefix, tenant string) string { return prefix + "-" + tenant + ".slice" }

// ManagedNetworkCreateArgs builds `docker network create` for a tenant. It is pure.
func ManagedNetworkCreateArgs(tenant string) []string {
	return []string{"network", "create", "--driver", "bridge",
		"--label", "nexal.tenant=" + tenant, "--label", "nexal.managed=1",
		"-o", "com.docker.network.bridge.name=" + ManagedBridgeName(tenant),
		ManagedNetworkName(tenant)}
}

// ManagedRunArgs are the dev container's docker run arguments in managed mode
// (they replace any runArgs the definition had). It is pure.
func ManagedRunArgs(c ManagedConfig, tenant, workspace string, size Size) []string {
	a := []string{"--network=" + ManagedNetworkName(tenant),
		"--label=nexal.tenant=" + tenant, "--label=nexal.workspace=" + workspace, "--label=nexal.managed=1"}
	if c.SlicePrefix != "" {
		a = append(a, "--cgroup-parent="+ManagedSliceName(c.SlicePrefix, tenant))
	}
	if size.CPUs > 0 {
		a = append(a, fmt.Sprintf("--cpus=%d", size.CPUs))
	}
	if size.MemoryMB > 0 {
		a = append(a, fmt.Sprintf("--memory=%dm", size.MemoryMB), fmt.Sprintf("--memory-swap=%dm", size.MemoryMB))
	}
	pids := c.PidsLimit
	if pids <= 0 {
		pids = 4096
	}
	a = append(a, fmt.Sprintf("--pids-limit=%d", pids))
	if c.StorageOpt && size.DiskGB > 0 {
		a = append(a, fmt.Sprintf("--storage-opt=size=%dG", size.DiskGB))
	}
	return a
}

// ManagedSidecarArgs are the extra `docker run` flags for the mesh sidecar in
// managed mode (it shares the dev container's network namespace, so it is on the
// tenant's network already). It is pure.
func ManagedSidecarArgs(c ManagedConfig, tenant string) []string {
	a := []string{"--label", "nexal.tenant=" + tenant, "--label", "nexal.managed=1",
		"--memory=384m", "--memory-swap=384m", "--pids-limit=512"}
	if c.SlicePrefix != "" {
		a = append(a, "--cgroup-parent="+ManagedSliceName(c.SlicePrefix, tenant))
	}
	return a
}

// managedAllowedKeys are the devcontainer.json properties a tenant may set on a
// managed host. Everything else is dropped. Docker Compose is refused outright.
var managedAllowedKeys = map[string]bool{
	"name": true, "image": true, "build": true, "features": true, "overrideFeatureInstallOrder": true,
	"customizations": true, "remoteUser": true, "containerUser": true, "updateRemoteUserUID": true,
	"containerEnv": true, "remoteEnv": true, "userEnvProbe": true, "workspaceFolder": true,
	"onCreateCommand": true, "updateContentCommand": true, "postCreateCommand": true,
	"postStartCommand": true, "postAttachCommand": true, "waitFor": true,
	"forwardPorts": true, "portsAttributes": true, "otherPortsAttributes": true,
	"overrideCommand": true, "shutdownAction": true, "hostRequirements": true,
}

// managedBuildKeys are the `build` properties kept (no build "options": they
// could pass --network=host or other daemon flags).
var managedBuildKeys = map[string]bool{"dockerfile": true, "context": true, "args": true, "target": true, "cacheFrom": true}

// SanitizeManagedDevcontainer rewrites a parsed devcontainer.json for a managed
// host: allowlisted properties only, build paths kept inside the source tree,
// and runArgs replaced by runArgs (the isolation arguments). configDir is the
// directory of the file relative to the source root ("." or ".devcontainer").
// It returns the dropped property names (sorted) for the log. It is pure.
func SanitizeManagedDevcontainer(m map[string]any, configDir string, runArgs []string) (map[string]any, []string, error) {
	if m == nil {
		return nil, nil, devErr(DevErrInvalid, "devcontainer.json must be an object")
	}
	if _, ok := m["dockerComposeFile"]; ok {
		return nil, nil, devErr(DevErrInvalid, "docker compose dev containers are not available on neXal storage")
	}
	out := map[string]any{}
	var dropped []string
	for k, v := range m {
		if managedAllowedKeys[k] {
			out[k] = v
		} else {
			dropped = append(dropped, k)
		}
	}
	if f, ok := out["features"]; ok {
		fm, ok := f.(map[string]any)
		if !ok {
			return nil, nil, devErr(DevErrInvalid, "features must be an object")
		}
		kept := map[string]any{}
		for id, opts := range fm {
			if ManagedFeatureAllowed(id) {
				kept[id] = opts
			} else {
				dropped = append(dropped, "features."+id)
			}
		}
		out["features"] = kept
	}
	if b, ok := out["build"]; ok {
		bm, ok := b.(map[string]any)
		if !ok {
			return nil, nil, devErr(DevErrInvalid, "build must be an object")
		}
		clean := map[string]any{}
		for k, v := range bm {
			if !managedBuildKeys[k] {
				dropped = append(dropped, "build."+k)
				continue
			}
			if k == "dockerfile" || k == "context" {
				s, ok := v.(string)
				if !ok || !insideTree(configDir, s) {
					return nil, nil, devErr(DevErrInvalid, "build.%s must stay inside the repository", k)
				}
			}
			clean[k] = v
		}
		out["build"] = clean
	}
	if _, hasImage := out["image"]; !hasImage {
		if _, hasBuild := out["build"]; !hasBuild {
			return nil, nil, devErr(DevErrInvalid, "devcontainer.json needs an image or a build")
		}
	}
	args := make([]any, len(runArgs))
	for i, a := range runArgs {
		args[i] = a
	}
	out["runArgs"] = args
	sort.Strings(dropped)
	return out, dropped, nil
}

// managedFeatureDenied are official features that need the docker socket or
// privileged mode (a feature's own metadata can ask for mounts, privileged and
// capAdd, which DevPod honours).
var managedFeatureDenied = map[string]bool{"docker-in-docker": true, "docker-outside-of-docker": true, "docker-from-docker": true}

// ManagedFeatureAllowed accepts only the official devcontainers features
// (ghcr.io/devcontainers/features/<name>[:tag]) minus the docker ones: a
// third-party feature could declare host mounts or privileged mode. It is pure.
func ManagedFeatureAllowed(id string) bool {
	const prefix = "ghcr.io/devcontainers/features/"
	if !strings.HasPrefix(id, prefix) {
		return false
	}
	name := strings.TrimPrefix(id, prefix)
	if i := strings.IndexAny(name, ":@"); i >= 0 {
		name = name[:i]
	}
	return name != "" && !strings.Contains(name, "/") && !managedFeatureDenied[name]
}

// insideTree reports whether rel (relative to configDir, itself relative to the
// source root) stays inside the source root. It is pure.
func insideTree(configDir, rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.ContainsAny(rel, "\x00\\") {
		return false
	}
	p := path.Clean(path.Join(configDir, rel))
	return p != ".." && !strings.HasPrefix(p, "../")
}

// StripJSONC removes // and /* */ comments and trailing commas (devcontainer.json
// is JSONC), leaving strings untouched. It is pure.
func StripJSONC(in []byte) []byte {
	var out []byte
	inStr, esc := false, false
	for i := 0; i < len(in); i++ {
		c := in[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(in) && in[i+1] == '/':
			for i < len(in) && in[i] != '\n' {
				i++
			}
			if i < len(in) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(in) && in[i+1] == '*':
			i += 2
			for i+1 < len(in) && !(in[i] == '*' && in[i+1] == '/') {
				i++
			}
			i++
		case c == ']' || c == '}':
			// Drop a trailing comma before the closer.
			j := len(out) - 1
			for j >= 0 && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
				j--
			}
			if j >= 0 && out[j] == ',' {
				out = append(out[:j], out[j+1:]...)
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

// managedConfigPaths are where a repository may keep its definition, in DevPod's order.
var managedConfigPaths = []string{".devcontainer/devcontainer.json", ".devcontainer.json"}

// GitCloneArgs clones one branch, shallow, with hooks and submodules off and
// only https allowed. It is pure.
func GitCloneArgs(repoURL, dest string) []string {
	return []string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.allow=never", "-c", "protocol.https.allow=always",
		"-c", "core.symlinks=false", "clone", "--depth", "1", "--single-branch", "--no-recurse-submodules", "--", repoURL, dest}
}

// prepareManagedSource builds the sanitized local source tree for a managed
// workspace in src and returns the dropped properties. Template and inline JSON
// sources are rendered; a repository is cloned, and its definition (or, if it has
// none, the ubuntu template) is sanitized in place. With reuse (a kept persistent
// workspace restarting), nothing is cloned or removed, but the definition in src
// is sanitized AGAIN: src is the workspace mounted in the container, so its owner
// may have edited it since.
func (d *DevPod) prepareManagedSource(ctx context.Context, t devTools, s DevUpSpec, src string, reuse bool) ([]string, error) {
	mc := *d.cfg.Managed
	runArgs := ManagedRunArgs(mc, s.Tenant, s.Workspace, s.Size)
	var m map[string]any
	configRel := managedConfigPaths[0]
	// src may be written by the container: never follow a symlink in it.
	if fi, err := os.Lstat(filepath.Join(src, ".devcontainer")); err == nil && !fi.IsDir() {
		_ = os.Remove(filepath.Join(src, ".devcontainer"))
	}
	readExisting := func() error {
		for _, rel := range managedConfigPaths {
			p := filepath.Join(src, filepath.FromSlash(rel))
			if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
				_ = os.Remove(p) // a symlink or other non-file is dropped, never read
				continue
			}
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			if len(b) > maxDevcontainerJSON || json.Unmarshal(StripJSONC(b), &m) != nil || m == nil {
				return devErr(DevErrInvalid, "%s is not a JSON object", rel)
			}
			configRel = rel
			return nil
		}
		return nil
	}
	if reuse {
		if err := readExisting(); err != nil {
			return nil, err
		}
	}
	if m == nil {
		switch {
		case s.Devcontainer.Template != "":
			img, ok := templateImages[s.Devcontainer.Template]
			if !ok {
				return nil, devErr(DevErrInvalid, "unknown template %q", s.Devcontainer.Template)
			}
			m = map[string]any{"name": "nexal-" + s.Devcontainer.Template, "image": img}
		case s.Devcontainer.JSON != "":
			if err := json.Unmarshal(StripJSONC([]byte(s.Devcontainer.JSON)), &m); err != nil || m == nil {
				return nil, devErr(DevErrInvalid, "devcontainer json must be an object")
			}
		case s.Devcontainer.RepoURL != "":
			if !reuse {
				git, ok := d.cfg.Env.FindBinary("git")
				if !ok {
					return nil, devErr(DevErrNotAvailable, "git is not installed on this server")
				}
				if out, err := d.cfg.Env.Run(ctx, []string{"GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1"}, git, GitCloneArgs(s.Devcontainer.RepoURL, src)...); err != nil {
					return nil, devErr(DevErrDevPodFailed, "cloning the repository failed: %s", trimOutput(out))
				}
				if err := readExisting(); err != nil {
					return nil, err
				}
			}
			if m == nil {
				m = map[string]any{"name": "nexal-ubuntu", "image": templateImages["ubuntu"]}
			}
		default:
			return nil, devErr(DevErrInvalid, "devcontainer source required")
		}
	}
	clean, dropped, err := SanitizeManagedDevcontainer(m, path.Dir(configRel), runArgs)
	if err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(clean, "", "  ")
	if err != nil {
		return nil, devErr(DevErrInvalid, "cannot render devcontainer.json")
	}
	target := filepath.Join(src, filepath.FromSlash(configRel))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return nil, err
	}
	// Only one definition may remain, so DevPod cannot pick an unsanitized one.
	for _, rel := range managedConfigPaths {
		if rel != configRel {
			_ = os.Remove(filepath.Join(src, filepath.FromSlash(rel)))
		}
	}
	// Replace rather than write through: a symlink planted at the target must not
	// redirect the write (core.symlinks=false already makes clones plain files).
	_ = os.Remove(target)
	if err := os.WriteFile(target, append(b, '\n'), 0o600); err != nil {
		return nil, err
	}
	return dropped, nil
}

// ensureManagedNetwork creates the tenant's network unless it exists.
func (d *DevPod) ensureManagedNetwork(ctx context.Context, t devTools, tenant string) error {
	if _, err := d.run(ctx, t, t.docker, "network", "inspect", "--format", "{{.Name}}", ManagedNetworkName(tenant)); err == nil {
		return nil
	}
	if out, err := d.run(ctx, t, t.docker, ManagedNetworkCreateArgs(tenant)...); err != nil && !strings.Contains(strings.ToLower(out), "already exists") {
		return devErr(DevErrNoRuntime, "creating the tenant network failed: %s", trimOutput([]byte(out)))
	}
	return nil
}
