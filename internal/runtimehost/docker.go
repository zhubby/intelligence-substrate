package runtimehost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

type ContainerDriver interface {
	Load(context.Context, string, string) error
	Start(context.Context, Deployment) (Deployment, error)
	Stop(context.Context, Deployment) error
}
type Docker struct{ Config Config }

func (d Docker) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, d.Config.DockerBinary, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "DOCKER_HOST=" + d.Config.DockerHost}
	var out bytes.Buffer
	cmd.Stdout = &out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 3 * time.Second
	if e := cmd.Run(); e != nil {
		return nil, fmt.Errorf("Docker %s failed; inspect the trusted runtime node", args[0])
	}
	if out.Len() > 4<<20 {
		return nil, fmt.Errorf("Docker response exceeds limit")
	}
	return out.Bytes(), nil
}
func (d Docker) Load(ctx context.Context, archive, id string) error {
	if _, e := d.run(ctx, "image", "load", "--input", archive); e != nil {
		return e
	}
	out, e := d.run(ctx, "image", "inspect", "--format", "{{.Id}}", id)
	if e != nil || string(bytes.TrimSpace(out)) != id {
		return fmt.Errorf("loaded image differs from approved image config")
	}
	return nil
}

type containerInspect struct {
	ID     string
	Config struct {
		Labels map[string]string
		Image  string
	}
	Image           string
	State           struct{ Running bool }
	NetworkSettings struct {
		Networks map[string]struct{ IPAddress string }
		Ports    map[string][]struct{ HostIP, HostPort string }
	}
}

func (d Docker) inspect(ctx context.Context, dep Deployment) (containerInspect, error) {
	var value containerInspect
	body, e := d.run(ctx, "container", "inspect", dep.Container)
	if e != nil {
		return value, e
	}
	var list []containerInspect
	if e = json.Unmarshal(body, &list); e != nil || len(list) != 1 {
		return value, fmt.Errorf("container inspect invalid")
	}
	value = list[0]
	if value.Config.Labels["org.intelligence-substrate.owner"] != d.Config.owner() || value.Config.Labels["org.intelligence-substrate.activation"] != dep.ID || value.Image != dep.ImageID {
		return value, fmt.Errorf("container is not owned by this approved activation")
	}
	return value, nil
}
func (d Docker) ownedResource(ctx context.Context, kind, name string) error {
	body, e := d.run(ctx, kind, "inspect", name)
	if e == nil {
		var list []struct{ Labels map[string]string }
		if json.Unmarshal(body, &list) != nil || len(list) != 1 || list[0].Labels["org.intelligence-substrate.owner"] != d.Config.owner() {
			return fmt.Errorf("runtime resource name already belongs to another owner")
		}
		return nil
	}
	args := []string{kind, "create", "--label", "org.intelligence-substrate.owner=" + d.Config.owner()}
	if kind == "network" {
		args = append(args, "--internal")
	}
	args = append(args, name)
	if _, e = d.run(ctx, args...); e != nil {
		return e
	}
	body, e = d.run(ctx, kind, "inspect", name)
	if e != nil {
		return e
	}
	var list []struct{ Labels map[string]string }
	if json.Unmarshal(body, &list) != nil || len(list) != 1 || list[0].Labels["org.intelligence-substrate.owner"] != d.Config.owner() {
		return fmt.Errorf("runtime resource owner could not be confirmed")
	}
	return nil
}
func (d Docker) Start(ctx context.Context, dep Deployment) (Deployment, error) {
	if _, e := d.inspect(ctx, dep); e == nil {
		if _, e = d.run(ctx, "container", "start", dep.Container); e != nil {
			return dep, e
		}
	} else {
		// A colliding existing container is never removed or repurposed. Docker's
		// create-name uniqueness fails safely if inspect was denied or mismatched.
		network := "isrt-" + d.Config.owner()
		if e = d.ownedResource(ctx, "network", network); e != nil {
			return dep, e
		}
		args := []string{"container", "create", "--pull=never", "--name", dep.Container, "--label", "org.intelligence-substrate.owner=" + d.Config.owner(), "--label", "org.intelligence-substrate.activation=" + dep.ID, "--network", network, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user", "10001:10001", "--pids-limit=128", "--memory", fmt.Sprintf("%dm", d.Config.MemoryMB), "--cpus", strconv.FormatFloat(d.Config.CPUs, 'f', -1, 64), "--restart=unless-stopped", "--log-opt", "max-size=10m", "--log-opt", "max-file=3", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m,uid=10001,gid=10001,mode=0700"}
		if d.Config.PersistentData {
			volume := "isrt-" + d.Config.owner() + "-data"
			if e = d.ownedResource(ctx, "volume", volume); e != nil {
				return dep, e
			}
			args = append(args, "--mount", "type=volume,source="+volume+",target=/data")
		}
		if d.Config.EnvironmentFile != "" {
			args = append(args, "--env-file", d.Config.EnvironmentFile)
		}
		args = append(args, dep.ImageID)
		if _, e = d.run(ctx, args...); e != nil {
			return dep, e
		}
		if _, e = d.run(ctx, "container", "start", dep.Container); e != nil {
			return dep, e
		}
	}
	value, e := d.inspect(ctx, dep)
	if e != nil || !value.State.Running {
		return dep, fmt.Errorf("approved container is not running")
	}
	if len(value.NetworkSettings.Networks) != 1 {
		return dep, fmt.Errorf("application must use only its managed internal network")
	}
	for _, bindings := range value.NetworkSettings.Ports {
		if len(bindings) > 0 {
			return dep, fmt.Errorf("application must not publish host ports")
		}
	}
	dep.Address = value.NetworkSettings.Networks["isrt-"+d.Config.owner()].IPAddress
	dep.Port = d.Config.ContainerPort
	if dep.Address == "" {
		return dep, fmt.Errorf("managed private container address unavailable")
	}
	if _, e = backendAddress(dep); e != nil {
		return dep, e
	}
	return dep, nil
}
func (d Docker) Stop(ctx context.Context, dep Deployment) error {
	value, e := d.inspect(ctx, dep)
	if e != nil {
		body, listErr := d.run(ctx, "container", "ls", "--all", "--filter", "name=^/"+dep.Container+"$", "--format", "{{.ID}}")
		if listErr == nil && len(bytes.TrimSpace(body)) == 0 {
			return nil
		}
		return e
	}
	if !value.State.Running {
		return nil
	}
	_, e = d.run(ctx, "container", "stop", "--time", "10", dep.Container)
	return e
}
