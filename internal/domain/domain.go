// Package domain holds the identity of rodin3d-mcp and the places it keeps
// its files. It talks to nothing; the Hyper3D client is internal/rodin.
package domain

import (
	"fmt"
	"os"
	"path/filepath"
)

// Identity used by install, doctor and update. Owner and Repo point at the
// GitHub repository that publishes releases.
const (
	ServerName = "rodin3d"
	BinaryName = "rodin3d-mcp"
	Owner      = "sairaph"
	Repo       = "rodin3d-mcp"
)

// AssetName maps a GOOS/GOARCH pair to the release asset published by
// .goreleaser.yml, which names binaries <project>-<os>-<arch>[.exe].
func AssetName(goos, goarch string) string {
	name := fmt.Sprintf("%s-%s-%s", BinaryName, goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// Paths are every file and folder rodin3d-mcp writes, all under one root.
type Paths struct {
	Root            string
	Config          string // config.toml: the [permission] table
	Credentials     string // credentials.json: tokens and account, mode 0600
	CredentialsLock string // flock taken around every token refresh
	Client          string // client.json: the registered OAuth client, kept across logouts
	PendingLogin    string // pending-login.json: PKCE state of a sign-in in progress
	RevokePending   string // revoke-pending.json: a grant whose revoke failed, retried by logout
	Database        string // rodin.db: tasks, cards, events, files, sync state
	DaemonLock      string
	DaemonPID       string
	DaemonLog       string
	Cache           string // cache/<task>/<step>-<version>/: stage images
	Downloads       string // downloads/<id8>-<slug>/: packed model files
	Previews        string // previews/: PNGs written by get_preview
	Viewer          string // viewer/: the viewer window's socket, lock, PID, log and cameras
	ViewerPID       string
	ViewerLog       string
	ViewerCameras   string // viewer/cameras.json: the last camera per task
}

// PathsUnder builds every path under root.
func PathsUnder(root string) Paths {
	return Paths{
		Root:            root,
		Config:          filepath.Join(root, "config.toml"),
		Credentials:     filepath.Join(root, "credentials.json"),
		CredentialsLock: filepath.Join(root, "credentials.lock"),
		Client:          filepath.Join(root, "client.json"),
		PendingLogin:    filepath.Join(root, "pending-login.json"),
		RevokePending:   filepath.Join(root, "revoke-pending.json"),
		Database:        filepath.Join(root, "rodin.db"),
		DaemonLock:      filepath.Join(root, "daemon.lock"),
		DaemonPID:       filepath.Join(root, "daemon.pid"),
		DaemonLog:       filepath.Join(root, "daemon.log"),
		Cache:           filepath.Join(root, "cache"),
		Downloads:       filepath.Join(root, "downloads"),
		Previews:        filepath.Join(root, "previews"),
		Viewer:          filepath.Join(root, "viewer"),
		ViewerPID:       filepath.Join(root, "viewer", "viewer.pid"),
		ViewerLog:       filepath.Join(root, "viewer", "viewer.log"),
		ViewerCameras:   filepath.Join(root, "viewer", "cameras.json"),
	}
}

// DefaultRoot is ~/.rodin3d-mcp (a relative .rodin3d-mcp when the home
// folder cannot be determined).
func DefaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "." + BinaryName
	}
	return filepath.Join(home, "."+BinaryName)
}

// DefaultPaths is PathsUnder(DefaultRoot()).
func DefaultPaths() Paths { return PathsUnder(DefaultRoot()) }

// CredentialPath returns the path where credentials are stored.
func CredentialPath() string { return DefaultPaths().Credentials }

// DefaultEnv returns the environment variables written into AI client
// configs when the server is registered. The server reads TRANSPORT (stdio
// or http) and ADDR (listen address for http) at startup.
func DefaultEnv() map[string]string {
	return map[string]string{
		"TRANSPORT": "stdio",
	}
}
