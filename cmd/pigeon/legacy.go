package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"

	"github.com/royvandam/pigeon/pkg/database"
	"github.com/royvandam/pigeon/pkg/server"
)

// checkLegacySettings handles the server settings that versions before
// configuration files saved in the database. The configuration must carry them
// over before Pigeon starts, so an upgrade cannot quietly serve another upload
// folder, listen elsewhere, or drop its HTTPS and proxy settings. Once they
// match, they are removed from the database, and the configuration file alone
// decides.
func checkLegacySettings(ctx context.Context, db *sql.DB, cfg ApplicationConfiguration) error {
	saved, err := database.ReadLegacySettings(ctx, db)
	if err != nil || saved == nil {
		return err
	}
	var savedProxies []string
	for _, proxy := range strings.Split(saved.TrustedProxies, ",") {
		if proxy = strings.TrimSpace(proxy); proxy != "" {
			savedProxies = append(savedProxies, proxy)
		}
	}
	savedHTTP, err := server.Configuration{
		ListenAddress: saved.ListenAddress, PublicURL: saved.PublicURL, TrustedProxies: savedProxies,
	}.Normalize()
	if err != nil {
		return fmt.Errorf("the server settings saved in the database are invalid: %w", err)
	}
	configuredHTTP, err := cfg.HTTP.Normalize()
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	directory := ""
	if cfg.Storage.Local != nil {
		if directory, err = filepath.Abs(cfg.Storage.Local.Directory); err != nil {
			return err
		}
	}

	var differences []string
	differ := func(name string, saved, configured any) {
		differences = append(differences, fmt.Sprintf("%s: %s (configured: %s)", name, jsonValue(saved), jsonValue(configured)))
	}
	if filepath.Clean(saved.UploadDirectory) != directory {
		differ("storage.local.directory", saved.UploadDirectory, directory)
	}
	if savedHTTP.ListenAddress != configuredHTTP.ListenAddress {
		differ("http.listenAddress", saved.ListenAddress, configuredHTTP.ListenAddress)
	}
	if savedHTTP.PublicURL != configuredHTTP.PublicURL {
		differ("http.publicURL", saved.PublicURL, cfg.HTTP.PublicURL)
	}
	if !slices.Equal(savedHTTP.TrustedProxies, configuredHTTP.TrustedProxies) {
		differ("http.trustedProxies", savedProxies, cfg.HTTP.TrustedProxies)
	}
	if len(differences) > 0 {
		return fmt.Errorf("the database holds server settings saved by an earlier version of Pigeon, "+
			"which now come from the configuration file. Set them there to continue; Pigeon then removes "+
			"them from the database, and they can be changed afterwards:\n\t%s", strings.Join(differences, "\n\t"))
	}
	if err := database.DropLegacySettings(ctx, db); err != nil {
		return err
	}
	log.Print("removed the server settings saved in the database by an earlier version; the configuration file holds them")
	return nil
}

// jsonValue writes a setting as the configuration file would hold it.
func jsonValue(value any) string {
	if values, ok := value.([]string); ok && values == nil {
		value = []string{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}
