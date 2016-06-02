package volplugin

import (
	"strings"

	"golang.org/x/net/context"

	"github.com/contiv/errored"
	"github.com/contiv/volplugin/config"
	"github.com/contiv/volplugin/lock"
	"github.com/contiv/volplugin/storage/backend"
	"github.com/docker/engine-api/client"
	"github.com/docker/engine-api/types"

	log "github.com/Sirupsen/logrus"
)

func (dc *DaemonConfig) updateMounts() error {
	dockerClient, err := client.NewEnvClient()
	if err != nil {
		return errored.Errorf("Could not initiate docker client").Combine(err)
	}

	containers, err := dockerClient.ContainerList(context.Background(), types.ContainerListOptions{})
	if err != nil {
		return errored.Errorf("Could not query docker").Combine(err)
	}

	for _, container := range containers {
		for _, mount := range container.Mounts {
			if mount.Driver != "volplugin" {
				continue
			}

			// FIXME docker needs to be polled to accomodate this functionality, or we need to write it to etcd.
			dc.increaseMount(mount.Name)
		}
	}

	for driverName := range backend.MountDrivers {
		cd, err := backend.NewMountDriver(driverName, dc.Global.MountPath)
		if err != nil {
			return err
		}

		mounts, err := cd.Mounted(dc.Global.Timeout)
		if err != nil {
			return err
		}

		for _, mount := range mounts {
			parts := strings.Split(mount.Volume.Name, "/")
			if len(parts) != 2 {
				log.Warnf("Invalid volume named %q in mount scan: skipping refresh", mount.Volume.Name)
				continue
			}

			log.Infof("Refreshing existing mount for %q", mount.Volume.Name)

			vol, err := dc.requestVolume(parts[0], parts[1])
			switch err {
			case errVolumeNotFound:
				log.Warnf("Volume %q not found in database, skipping")
				continue
			case errVolumeResponse:
				log.Fatalf("Volmaster could not be contacted; aborting volplugin.")
			}

			payload := &config.UseMount{
				Volume:   mount.Volume.Name,
				Reason:   lock.ReasonMount,
				Hostname: dc.Host,
			}

			if vol.Unlocked {
				payload.Hostname = lock.Unlocked
			}

			if err := dc.Client.ReportMount(payload); err != nil {
				if err := dc.Client.ReportMountStatus(payload); err != nil {
					// FIXME everything is effed up. what should we really be doing here?
					return err
				}
			}

			go dc.startRuntimePoll(mount.Volume.Name, mount)
			go dc.Client.HeartbeatMount(dc.Global.TTL, payload, dc.Client.AddStopChan(mount.Volume.Name))
		}
	}

	return nil
}