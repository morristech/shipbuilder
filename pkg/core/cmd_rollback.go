package core

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/jaytaylor/shipbuilder/pkg/domain"
)

func (server *Server) Rollback(conn net.Conn, applicationName, version string) error {
	return server.WithApplication(applicationName, func(app *Application, cfg *Config) error {
		deployLock.start()
		defer deployLock.finish()

		if app.LastDeploy == "" {
			return errors.New("Automatic rollback version detection is impossible because this app has not had any releases")
		}
		if app.LastDeploy == "v1" {
			return errors.New("Automatic rollback version detection is impossible because this app has only had 1 release")
		}
		if version == "" {
			// Get release before current.
			var err error = nil
			version, err = app.CalcPreviousVersion()
			if err != nil {
				return err
			}
		}
		logger := NewLogger(NewTimeLogger(NewMessageLogger(conn)), "[rollback] ")
		fmt.Fprintf(logger, "Rolling back to %v\n", version)

		// Get the next version.
		app, cfg, err := server.IncrementAppVersion(app)
		if err != nil {
			return err
		}

		deployment := NewDeployment(DeploymentOptions{
			Server:      server,
			Logger:      logger,
			Config:      cfg,
			Application: app,
			Version:     app.LastDeploy,
			StartedTs:   time.Now(),
		})

		// Cleanup any hanging chads upon error.
		defer func() {
			if err != nil {
				deployment.undoVersionBump()
			}
		}()

		var release domain.Release

		if release, err = deployment.ResolveEarliestReleaseAlias(version); err != nil {
			return err
		}

		// Avoid LXC complaints about duplicate fingerprints.
		if version != release.Version {
			fmt.Fprintf(logger, "Found original version of %v => %v, will use the original for fingerprint=%v", version, release.Version, release.ImageFingerprint)
			version = release.Version
		}

		if err := deployment.extract(version); err != nil {
			return err
		}
		if err := deployment.archive(); err != nil {
			return err
		}
		if err := deployment.deploy(); err != nil {
			return err
		}
		return nil
	})
}

// ResolveEarliestReleaseAlias finds the root (earliest) version with image
// fingerprint matching the requested version.
func (d *Deployment) ResolveEarliestReleaseAlias(version string) (domain.Release, error) {
	releases, err := d.Server.ReleasesProvider.List(d.Application.Name)
	if err != nil {
		return domain.Release{}, err
	}

	var fingerprint string

	// More recent releases are more likely to be what is requested, so iterate
	// from newest to oldest (releases are already in order from newest to
	// oldest).
	for _, release := range releases {
		if release.Version == version {
			if release.ImageFingerprint == "" {
				return domain.Release{}, errors.New("no fingerprint found for requested version")
			}
			fingerprint = release.ImageFingerprint
			break
		}
	}

	if fingerprint == "" {
		return domain.Release{}, errors.New("requested version not found")
	}

	// Now iterate from oldest to newest to find the first occurrence of matching
	// fingerprint.
	for i := len(releases) - 1; i >= 0; i-- {
		if releases[i].ImageFingerprint == fingerprint {
			return releases[i], nil
		}
	}

	return domain.Release{}, fmt.Errorf("this should never happen, but somehow no fingerprints matching %q were found", fingerprint)
}
