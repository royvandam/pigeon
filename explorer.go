package main

import (
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
)

type fileEntry struct {
	Name        string
	Path        string
	Directory   bool
	Size        string
	Modified    string
	BrowseURL   string
	DownloadURL string
}

type breadcrumb struct {
	Name string
	URL  string
}

type explorerData struct {
	Directory   string
	Entries     []fileEntry
	Breadcrumbs []breadcrumb
	CSRFToken   string
}

func explorerURL(directory string) string {
	if directory == "" {
		return "/"
	}
	return "/?" + url.Values{"dir": {directory}}.Encode()
}

func readExplorer(root *os.Root, directory, csrf string) (explorerData, error) {
	data := explorerData{Directory: directory, CSRFToken: csrf}
	if err := requireDirectory(root, directory); err != nil {
		return data, err
	}
	listingPath := directory
	if listingPath == "" {
		listingPath = "."
	}
	entries, err := fs.ReadDir(root.FS(), listingPath)
	if err != nil {
		return data, err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), stagingPrefix) {
			continue
		}
		name := path.Join(directory, entry.Name())
		info, err := storageInfo(root, name)
		if errors.Is(err, os.ErrNotExist) { // Another request may have moved it.
			continue
		}
		if err != nil {
			return data, err
		}
		size := "—"
		if !info.IsDir() {
			size = humanBytes(info.Size())
		}
		data.Entries = append(data.Entries, fileEntry{
			Name: entry.Name(), Path: name, Directory: info.IsDir(), Size: size,
			Modified:    info.ModTime().Local().Format("2006-01-02 15:04"),
			BrowseURL:   explorerURL(name),
			DownloadURL: "/download?" + url.Values{"path": {name}}.Encode(),
		})
	}
	sort.Slice(data.Entries, func(i, j int) bool {
		a, b := data.Entries[i], data.Entries[j]
		if a.Directory != b.Directory {
			return a.Directory
		}
		if strings.ToLower(a.Name) == strings.ToLower(b.Name) {
			return a.Name < b.Name
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	data.Breadcrumbs = append(data.Breadcrumbs, breadcrumb{Name: "My files", URL: "/"})
	if directory != "" {
		prefix := ""
		for _, component := range strings.Split(directory, "/") {
			prefix = path.Join(prefix, component)
			data.Breadcrumbs = append(data.Breadcrumbs, breadcrumb{Name: component, URL: explorerURL(prefix)})
		}
	}
	return data, nil
}
